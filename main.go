package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int commandCodePluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void commandCodePluginFree(void*, size_t);
extern void commandCodePluginShutdown(void);

static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
	stored_host = host;
}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) {
		return 1;
	}
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) {
		stored_host->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

const wireCheckInterval = 24 * time.Hour

var wireDriftCheckTimeout = 10 * time.Second

var wireCheckState struct {
	sync.Mutex
	lastCheck time.Time
}

var wireDriftHostCall = func(_ context.Context, payload []byte) ([]byte, error) {
	return callHost(pluginabi.MethodHostHTTPDo, payload)
}

var wireDriftLog = logHost

type hostLogRequest struct {
	Level   string         `json:"level,omitempty"`
	Message string         `json:"message,omitempty"`
	Fields  map[string]any `json:"fields,omitempty"`
}

type hostHTTPDoRequest struct {
	Method  string              `json:"method"`
	URL     string              `json:"url"`
	Headers map[string][]string `json:"headers,omitempty"`
}

type hostHTTPResponse struct {
	StatusCode      int    `json:"StatusCode"`
	StatusCodeSnake int    `json:"status_code"`
	Body            []byte `json:"Body"`
	BodySnake       []byte `json:"body"`
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}

	C.store_host_api(host)
	plugin.abi_version = C.uint32_t(verifiedABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.commandCodePluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.commandCodePluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.commandCodePluginShutdown)

	if host == nil {
		fmt.Fprintln(os.Stderr, "[command-code] CLIProxyAPI host API is unavailable")
	}

	return 0
}

//export commandCodePluginCall
func commandCodePluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}

	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}

	raw, err := handleMethod(C.GoString(method), requestBytes)
	if err != nil {
		var pluginErr *pluginabi.Error
		if errors.As(err, &pluginErr) {
			writeResponse(response, errorEnvelope(pluginErr.Code, pluginErr.Message, pluginErr.HTTPStatus))
		} else {
			writeResponse(response, errorEnvelope("plugin_error", err.Error()))
		}
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export commandCodePluginFree
func commandCodePluginFree(ptr unsafe.Pointer, length C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
	_ = length
}

//export commandCodePluginShutdown
func commandCodePluginShutdown() {
	commandCodePluginLifecycle.quiesceAndWait()
	commandCodeHostCallbackBoundary.wait()
	clearCredentialState()
	wireCheckState.Lock()
	wireCheckState.lastCheck = time.Time{}
	wireCheckState.Unlock()
	C.store_host_api(nil)
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}

	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}

func callHost(method string, payload []byte) ([]byte, error) {
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))

	var request *C.uint8_t
	if len(payload) > 0 {
		request = (*C.uint8_t)(C.CBytes(payload))
		defer C.free(unsafe.Pointer(request))
	}

	var response C.cliproxy_buffer
	if C.call_host_api(cMethod, request, C.size_t(len(payload)), &response) != 0 {
		return nil, fmt.Errorf("host callback failed: %s", method)
	}
	if response.ptr == nil || response.len == 0 {
		return nil, nil
	}

	raw := C.GoBytes(response.ptr, C.int(response.len))
	C.free_host_buffer(response.ptr, response.len)

	var envelope pluginabi.Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, errors.New("invalid host callback response")
	}
	if !envelope.OK {
		if envelope.Error == nil {
			return nil, errors.New("host callback returned an error")
		}
		return nil, errors.New(envelope.Error.Message)
	}
	return append([]byte(nil), envelope.Result...), nil
}

func logHost(level, message string, fields map[string]any) {
	payload, err := json.Marshal(hostLogRequest{Level: level, Message: message, Fields: fields})
	if err == nil {
		if _, callbackErr := callHost(pluginabi.MethodHostLog, payload); callbackErr == nil {
			return
		}
	}
	fmt.Fprintf(os.Stderr, "[command-code] %s: %s\n", level, message)
}

func callWireDriftHost(ctx context.Context, payload []byte) ([]byte, error) {
	return invokeBoundedHostCallback(ctx, func() ([]byte, error) {
		return wireDriftHostCall(ctx, payload)
	})
}

func scheduleWireDriftCheck() {
	now := time.Now()
	wireCheckState.Lock()
	if !wireCheckState.lastCheck.IsZero() && now.Sub(wireCheckState.lastCheck) < wireCheckInterval {
		wireCheckState.Unlock()
		return
	}
	wireCheckState.lastCheck = now
	wireCheckState.Unlock()

	go checkWireDrift()
}

func checkWireDrift() {
	release, err := commandCodePluginLifecycle.begin()
	if err != nil {
		return
	}
	defer release()

	payload, err := json.Marshal(hostHTTPDoRequest{
		Method: "GET",
		URL:    driftCheckURL,
		Headers: map[string][]string{
			"accept": {"application/json"},
		},
	})
	if err != nil {
		wireDriftLog("warn", "Command Code wire version check could not be prepared", nil)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), wireDriftCheckTimeout)
	defer cancel()
	raw, err := callWireDriftHost(ctx, payload)
	if err != nil {
		message := "Command Code wire version check failed"
		fields := map[string]any{"policy": "warn-only"}
		if errors.Is(err, context.DeadlineExceeded) {
			message = "Command Code wire version check timed out"
			fields["timeout"] = wireDriftCheckTimeout.String()
		}
		wireDriftLog("warn", message, fields)
		return
	}

	var response hostHTTPResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		wireDriftLog("warn", "Command Code wire version check returned an invalid response", map[string]any{
			"policy": "warn-only",
		})
		return
	}
	status := response.StatusCode
	if status == 0 {
		status = response.StatusCodeSnake
	}
	if status < 200 || status >= 300 {
		wireDriftLog("warn", "Command Code wire version check returned a non-success status", map[string]any{
			"status": status,
			"policy": "warn-only",
		})
		return
	}

	body := response.Body
	if len(body) == 0 {
		body = response.BodySnake
	}
	var packageInfo struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &packageInfo); err != nil || packageInfo.Version == "" {
		wireDriftLog("warn", "Command Code wire version check returned no version", map[string]any{
			"policy": "warn-only",
		})
		return
	}
	if packageInfo.Version != verifiedCommandCodeWireVersion {
		wireDriftLog("warn", "Command Code wire version drift detected; keeping the verified wire format", map[string]any{
			"implemented": verifiedCommandCodeWireVersion,
			"latest":      packageInfo.Version,
			"policy":      "warn-only",
		})
		return
	}
	wireDriftLog("info", "Command Code wire version in sync", map[string]any{
		"version": verifiedCommandCodeWireVersion,
	})
}
