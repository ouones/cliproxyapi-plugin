package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultCommandCodeAPIBase  = "https://api.commandcode.ai"
	commandCodeFingerprintRoot = "command-code:device-fingerprint:v1"
	commandCodeSessionDuration = 12 * time.Hour
	commandCodeSessionJitter   = time.Hour
	commandCodeInitRefresh     = 8 * time.Hour
	commandCodeInitJitter      = 2 * time.Hour
	commandCodeIdentityCleanup = time.Hour
)

var (
	fingerprintCPUs = []fingerprintCPU{
		{model: "12th Gen Intel(R) Core(TM) i7-12650H", cores: 10},
		{model: "12th Gen Intel(R) Core(TM) i5-12400F", cores: 6},
		{model: "12th Gen Intel(R) Core(TM) i9-12900K", cores: 16},
		{model: "13th Gen Intel(R) Core(TM) i7-13700K", cores: 16},
		{model: "13th Gen Intel(R) Core(TM) i5-13600K", cores: 14},
		{model: "13th Gen Intel(R) Core(TM) i9-13900K", cores: 24},
		{model: "Intel(R) Core(TM) Ultra 7 155H", cores: 16},
		{model: "Intel(R) Core(TM) Ultra 9 285H", cores: 16},
		{model: "Intel(R) Core(TM) i9-14900K", cores: 24},
		{model: "Intel(R) Core(TM) i7-14700K", cores: 20},
		{model: "AMD Ryzen 7 7800X3D", cores: 8},
		{model: "AMD Ryzen 9 7950X", cores: 16},
		{model: "AMD Ryzen 5 7600", cores: 6},
		{model: "AMD Ryzen 9 7900X", cores: 12},
		{model: "AMD Ryzen 7 5800X3D", cores: 8},
	}
	fingerprintMemGiB    = []int{8, 16, 24, 32, 48, 64}
	fingerprintTimezones = []string{
		"America/New_York", "America/Chicago", "America/Los_Angeles", "America/Toronto",
		"Europe/London", "Europe/Berlin", "Europe/Paris", "Europe/Moscow",
		"Asia/Shanghai", "Asia/Tokyo", "Asia/Singapore", "Asia/Seoul", "Asia/Hong_Kong",
		"Australia/Sydney", "Pacific/Auckland",
	}
	fingerprintMACCounts   = []int{2, 3, 4, 5}
	fingerprintOSUsers     = []string{"dev", "user", "admin", "coder", "engineer", "work"}
	fingerprintMailDomains = []string{"gmail.com", "outlook.com", "qq.com", "163.com"}
)

type fingerprintCPU struct {
	model string
	cores int
}

type deviceFingerprint struct {
	Thumbmark  string                `json:"thumbmark"`
	Components fingerprintComponents `json:"components"`
}

type fingerprintComponents struct {
	MachineIDHash    string   `json:"machineIdHash"`
	MACHashes        []string `json:"macHashes"`
	OSUserHash       string   `json:"osUserHash"`
	HostnameHash     string   `json:"hostnameHash"`
	GitEmailHash     string   `json:"gitEmailHash"`
	Platform         string   `json:"platform"`
	Arch             string   `json:"arch"`
	OSRelease        string   `json:"osRelease"`
	CPUModel         string   `json:"cpuModel"`
	CPUCount         int      `json:"cpuCount"`
	MemGiB           int      `json:"memGiB"`
	IsContainer      bool     `json:"isContainer"`
	Timezone         string   `json:"timezone"`
	Runtime          string   `json:"runtime"`
	CollectorVersion int      `json:"collectorVersion"`
}

type identityStateConfig struct {
	fingerprintSalt string
	apiBase         string
	cliSessionMode  string
	now             func() time.Time
	randomDuration  func(time.Duration) time.Duration
	newSessionID    func() (string, error)
	newLifecycleID  func() (string, error)
	sessionDuration time.Duration
	sessionJitter   time.Duration
	initRefresh     time.Duration
	initJitter      time.Duration
	retention       time.Duration
	cleanupInterval time.Duration
}

func (c identityStateConfig) withDefaults() identityStateConfig {
	if c.apiBase == "" {
		c.apiBase = os.Getenv("CC_API_BASE")
		if c.apiBase == "" {
			c.apiBase = defaultCommandCodeAPIBase
		}
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.randomDuration == nil {
		c.randomDuration = cryptoRandomDuration
	}
	if c.newSessionID == nil {
		c.newSessionID = randomUUID
	}
	if c.newLifecycleID == nil {
		c.newLifecycleID = func() (string, error) {
			suffix, err := randomHex(8)
			if err != nil {
				return "", err
			}
			return "sess_" + suffix, nil
		}
	}
	if c.sessionDuration == 0 {
		c.sessionDuration = commandCodeSessionDuration
	}
	if c.sessionJitter == 0 {
		c.sessionJitter = commandCodeSessionJitter
	}
	if c.initRefresh == 0 {
		c.initRefresh = commandCodeInitRefresh
	}
	if c.initJitter == 0 {
		c.initJitter = commandCodeInitJitter
	}
	if c.retention == 0 {
		c.retention = maxDuration(c.sessionDuration+c.sessionJitter, c.initRefresh+c.initJitter)
	}
	if c.cleanupInterval == 0 {
		c.cleanupInterval = commandCodeIdentityCleanup
	}
	if c.cliSessionMode == "" {
		c.cliSessionMode = "interactive"
	}
	return c
}

type identityState struct {
	mu               sync.Mutex
	initMu           sync.Mutex
	fingerprint      deviceFingerprint
	sessionID        string
	sessionExpiresAt time.Time
	nextInitAt       time.Time
	lastUsedAt       time.Time
}

type identityStateStore struct {
	mu     sync.Mutex
	states map[string]*identityState
	config identityStateConfig
}

var commandCodeIdentityStates = newIdentityStateStore(identityStateConfig{
	fingerprintSalt: os.Getenv("CC_FINGERPRINT_SALT"),
})

func init() {
	commandCodeIdentityStates.startCleanup()
}

func newIdentityStateStore(config identityStateConfig) *identityStateStore {
	return &identityStateStore{
		states: make(map[string]*identityState),
		config: config.withDefaults(),
	}
}

func (s *identityStateStore) stateFor(apiKey string) (*identityState, error) {
	if apiKey == "" {
		return nil, errors.New("api key is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if state := s.states[apiKey]; state != nil {
		return state, nil
	}

	state := &identityState{
		fingerprint: generateDeviceFingerprint(s.config.fingerprintSalt, apiKey),
		lastUsedAt:  s.config.now(),
	}
	s.states[apiKey] = state
	return state, nil
}

func (s *identityStateStore) fingerprintFor(apiKey string) (deviceFingerprint, error) {
	state, err := s.stateFor(apiKey)
	if err != nil {
		return deviceFingerprint{}, err
	}

	now := s.config.now()
	state.mu.Lock()
	state.lastUsedAt = now
	fingerprint := cloneDeviceFingerprint(state.fingerprint)
	state.mu.Unlock()
	return fingerprint, nil
}

// sessionID follows the existing proxy priority: explicit session headers first,
// then prompt_cache_key, and only then a per-API-key internal UUID.
func (s *identityStateStore) sessionID(incomingHeaders map[string][]string, apiKey, promptCacheKey string) (string, error) {
	state, err := s.stateFor(apiKey)
	if err != nil {
		return "", err
	}

	now := s.config.now()
	for _, header := range []string{"x-session-id", "x-claude-code-session-id", "session_id"} {
		if id := validIncomingSessionID(headerValue(incomingHeaders, header)); id != "" {
			state.mu.Lock()
			state.lastUsedAt = now
			state.mu.Unlock()
			return id, nil
		}
	}
	if id := validIncomingSessionID(promptCacheKey); id != "" {
		state.mu.Lock()
		state.lastUsedAt = now
		state.mu.Unlock()
		return id, nil
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	state.lastUsedAt = now
	if state.sessionID != "" && now.Before(state.sessionExpiresAt) {
		return state.sessionID, nil
	}

	id, err := s.config.newSessionID()
	if err != nil {
		return "", fmt.Errorf("create Command Code session: %w", err)
	}
	state.sessionID = id
	state.sessionExpiresAt = now.Add(s.config.sessionDuration + boundedDuration(s.config.randomDuration(s.config.sessionJitter), s.config.sessionJitter))
	return id, nil
}

func (s *identityStateStore) reset() {
	s.mu.Lock()
	s.states = make(map[string]*identityState)
	s.mu.Unlock()
}

func (s *identityStateStore) cleanupExpired(now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	cleaned := 0
	for apiKey, state := range s.states {
		state.mu.Lock()
		lastUsedAt := state.lastUsedAt
		sessionActive := state.sessionID != "" && now.Before(state.sessionExpiresAt)
		initializationActive := !state.nextInitAt.IsZero() && now.Before(state.nextInitAt)
		state.mu.Unlock()

		if !sessionActive && !initializationActive && !lastUsedAt.IsZero() && now.Sub(lastUsedAt) >= s.config.retention {
			delete(s.states, apiKey)
			cleaned++
		}
	}
	return cleaned
}

func (s *identityStateStore) startCleanup() {
	if s.config.cleanupInterval <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(s.config.cleanupInterval)
		defer ticker.Stop()
		for now := range ticker.C {
			s.cleanupExpired(now)
		}
	}()
}

type identityHTTPClient interface {
	Do(context.Context, identityHTTPRequest) (identityHTTPResponse, error)
}

type identityHTTPRequest struct {
	Method  string
	URL     string
	Headers map[string][]string
	Body    []byte
}

type identityHTTPResponse struct {
	StatusCode int
}

type identityLogger func(level, message string, fields map[string]any)

// ensureInitialized is deliberately best-effort. Lifecycle telemetry is an
// identity side effect; its failure must never turn the model request into a
// failure. A failed refresh leaves nextInitAt unchanged so the next real call
// retries it.
func (s *identityStateStore) ensureInitialized(ctx context.Context, apiKey string, client identityHTTPClient, logger identityLogger) {
	state, err := s.stateFor(apiKey)
	if err != nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	state.initMu.Lock()
	defer state.initMu.Unlock()

	now := s.config.now()
	state.mu.Lock()
	state.lastUsedAt = now
	if now.Before(state.nextInitAt) {
		state.mu.Unlock()
		return
	}
	fingerprint := cloneDeviceFingerprint(state.fingerprint)
	state.mu.Unlock()

	requests, err := s.initializationRequests(apiKey, fingerprint)
	if err != nil {
		s.logInitializationWarning(logger, apiKey, "Command Code identity initialization could not be prepared", nil)
		return
	}
	if client == nil {
		s.logInitializationWarning(logger, apiKey, "Command Code identity initialization has no HTTP client", nil)
		return
	}

	type result struct {
		url    string
		status int
		err    error
	}
	results := make(chan result, len(requests))
	for _, request := range requests {
		go func(request identityHTTPRequest) {
			response, requestErr := client.Do(ctx, request)
			results <- result{url: request.URL, status: response.StatusCode, err: requestErr}
		}(request)
	}

	success := true
	statuses := make(map[string]int, len(requests))
	for range requests {
		result := <-results
		statuses[result.url] = result.status
		if result.err != nil || result.status < 200 || result.status >= 300 {
			success = false
		}
	}
	if !success {
		fields := map[string]any{"keyPrefix": redactAPIKey(apiKey), "policy": "warn-only"}
		if status, ok := statuses[requests[0].URL]; ok {
			fields["fingerprintStatus"] = status
		}
		if status, ok := statuses[requests[1].URL]; ok {
			fields["lifecycleStatus"] = status
		}
		safeIdentityLog(logger, "warn", "Command Code identity initialization failed", fields)
		return
	}

	jitter := boundedDuration(s.config.randomDuration(s.config.initJitter), s.config.initJitter)
	state.mu.Lock()
	state.nextInitAt = s.config.now().Add(s.config.initRefresh + jitter)
	state.lastUsedAt = s.config.now()
	state.mu.Unlock()
}

func (s *identityStateStore) initializationRequests(apiKey string, fingerprint deviceFingerprint) ([]identityHTTPRequest, error) {
	fingerprintBody, err := json.Marshal(fingerprint)
	if err != nil {
		return nil, fmt.Errorf("marshal fingerprint: %w", err)
	}
	lifecycleID, err := s.config.newLifecycleID()
	if err != nil {
		return nil, fmt.Errorf("create lifecycle session: %w", err)
	}
	lifecycleBody, err := json.Marshal(map[string]any{
		"eventType": "cli_session_exists",
		"metadata": map[string]any{
			"sessionId":  lifecycleID,
			"cliVersion": verifiedCommandCodeWireVersion,
			"mode":       s.config.cliSessionMode,
			"os":         "win32-x64",
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal lifecycle event: %w", err)
	}

	base := strings.TrimRight(s.config.apiBase, "/")
	headers := map[string][]string{
		"Content-Type":           {"application/json"},
		"x-cli-environment":      {"production"},
		"Authorization":          {"Bearer " + apiKey},
		"x-command-code-version": {verifiedCommandCodeWireVersion},
	}
	return []identityHTTPRequest{
		{Method: "POST", URL: base + "/alpha/fingerprint/record", Headers: cloneHeaders(headers), Body: fingerprintBody},
		{Method: "POST", URL: base + "/alpha/lifecycle-events", Headers: cloneHeaders(headers), Body: lifecycleBody},
	}, nil
}

func (s *identityStateStore) logInitializationWarning(logger identityLogger, apiKey, message string, fields map[string]any) {
	if fields == nil {
		fields = make(map[string]any)
	}
	fields["keyPrefix"] = redactAPIKey(apiKey)
	fields["policy"] = "warn-only"
	safeIdentityLog(logger, "warn", message, fields)
}

func safeIdentityLog(logger identityLogger, level, message string, fields map[string]any) {
	if logger != nil {
		logger(level, message, fields)
	}
}

func generateDeviceFingerprint(fingerprintSalt, apiKey string) deviceFingerprint {
	cpuIndex := fingerprintPickIndex(fingerprintSalt, apiKey, "cpu", len(fingerprintCPUs), func(i int) string {
		return fingerprintCPUs[i].model + "|" + fmt.Sprint(fingerprintCPUs[i].cores)
	})
	memIndex := fingerprintPickIndex(fingerprintSalt, apiKey, "mem", len(fingerprintMemGiB), func(i int) string {
		return fmt.Sprint(fingerprintMemGiB[i])
	})
	timezoneIndex := fingerprintPickIndex(fingerprintSalt, apiKey, "timezone", len(fingerprintTimezones), func(i int) string {
		return fingerprintTimezones[i]
	})
	macCountIndex := fingerprintPickIndex(fingerprintSalt, apiKey, "macCount", len(fingerprintMACCounts), func(i int) string {
		return fmt.Sprint(fingerprintMACCounts[i])
	})
	osUserIndex := fingerprintPickIndex(fingerprintSalt, apiKey, "osUser", len(fingerprintOSUsers), func(i int) string {
		return fingerprintOSUsers[i]
	})
	mailDomainIndex := fingerprintPickIndex(fingerprintSalt, apiKey, "mailDomain", len(fingerprintMailDomains), func(i int) string {
		return fingerprintMailDomains[i]
	})

	machineID := fingerprintHex(fingerprintSalt, apiKey, "machineId", 16)
	machineID = machineID[:8] + "-" + machineID[8:12] + "-" + machineID[12:16] + "-" + machineID[16:20] + "-" + machineID[20:32]
	macs := make([]string, 0, fingerprintMACCounts[macCountIndex])
	for i := 0; i < fingerprintMACCounts[macCountIndex]; i++ {
		macs = append(macs, colonHex(fingerprintDigest(fingerprintSalt, apiKey, fmt.Sprintf("mac%d", i))[:6]))
	}
	sort.Strings(macs)

	hostname := "DESKTOP-" + strings.ToUpper(fingerprintHex(fingerprintSalt, apiKey, "hostname", 4))
	osUser := fingerprintOSUsers[osUserIndex]
	gitEmail := osUser + "." + fingerprintHex(fingerprintSalt, apiKey, "gitEmail", 3) + "@" + fingerprintMailDomains[mailDomainIndex]

	thumbSeed := machineID + "|" + strings.Join(macs, ",")
	thumbmark := sha256Hex(commandCodeFingerprintRoot + "\x00machine\x00" + thumbSeed)
	return deviceFingerprint{
		Thumbmark: thumbmark,
		Components: fingerprintComponents{
			MachineIDHash:    fingerprintHashSignal(machineID),
			MACHashes:        mapFingerprintHashes(macs),
			OSUserHash:       fingerprintHashSignal(osUser),
			HostnameHash:     fingerprintHashSignal(hostname),
			GitEmailHash:     fingerprintHashSignal(gitEmail),
			Platform:         "win32",
			Arch:             "x64",
			OSRelease:        "10.0.22631",
			CPUModel:         fingerprintCPUs[cpuIndex].model,
			CPUCount:         fingerprintCPUs[cpuIndex].cores,
			MemGiB:           fingerprintMemGiB[memIndex],
			IsContainer:      false,
			Timezone:         fingerprintTimezones[timezoneIndex],
			Runtime:          "cli",
			CollectorVersion: 1,
		},
	}
}

func fingerprintDigest(fingerprintSalt, apiKey, field string) []byte {
	digest := sha256.Sum256([]byte(fingerprintSalt + "\x00" + apiKey + "\x00" + field))
	return digest[:]
}

func fingerprintPickIndex(fingerprintSalt, apiKey, field string, length int, label func(int) string) int {
	bestIndex := 0
	var bestScore []byte
	for i := 0; i < length; i++ {
		score := fingerprintDigest(fingerprintSalt, apiKey, field+"\x00"+label(i))
		if bestScore == nil || bytes.Compare(score, bestScore) > 0 {
			bestIndex = i
			bestScore = score
		}
	}
	return bestIndex
}

func fingerprintHex(fingerprintSalt, apiKey, field string, size int) string {
	return hex.EncodeToString(fingerprintDigest(fingerprintSalt, apiKey, field)[:size])
}

func fingerprintHashSignal(value string) string {
	return sha256Hex(commandCodeFingerprintRoot + "\x00" + strings.ToLower(strings.TrimSpace(value)))
}

func sha256Hex(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func mapFingerprintHashes(values []string) []string {
	hashes := make([]string, 0, len(values))
	for _, value := range values {
		hashes = append(hashes, fingerprintHashSignal(value))
	}
	return hashes
}

func cloneDeviceFingerprint(fingerprint deviceFingerprint) deviceFingerprint {
	fingerprint.Components.MACHashes = append([]string(nil), fingerprint.Components.MACHashes...)
	return fingerprint
}

func colonHex(value []byte) string {
	parts := make([]string, len(value))
	for i, b := range value {
		parts[i] = fmt.Sprintf("%02x", b)
	}
	return strings.Join(parts, ":")
}

func validIncomingSessionID(value string) string {
	if len(strings.TrimSpace(value)) < 8 {
		return ""
	}
	return value
}

func headerValue(headers map[string][]string, name string) string {
	for key, values := range headers {
		if !strings.EqualFold(key, name) {
			continue
		}
		for _, value := range values {
			if value != "" {
				return value
			}
		}
	}
	return ""
}

func cloneHeaders(headers map[string][]string) map[string][]string {
	clone := make(map[string][]string, len(headers))
	for key, values := range headers {
		clone[key] = append([]string(nil), values...)
	}
	return clone
}

func redactAPIKey(apiKey string) string {
	if len(apiKey) <= 8 {
		return "[redacted]"
	}
	return apiKey[:8]
}

func maxDuration(values ...time.Duration) time.Duration {
	var result time.Duration
	for _, value := range values {
		if value > result {
			result = value
		}
	}
	return result
}

func boundedDuration(value, max time.Duration) time.Duration {
	if value < 0 {
		return 0
	}
	if value > max {
		return max
	}
	return value
}

func cryptoRandomDuration(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	var bytes8 [8]byte
	if _, err := rand.Read(bytes8[:]); err != nil {
		return 0
	}
	return time.Duration(binary.BigEndian.Uint64(bytes8[:]) % uint64(max))
}

func randomHex(size int) (string, error) {
	bytesValue := make([]byte, size)
	if _, err := rand.Read(bytesValue); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytesValue), nil
}

func randomUUID() (string, error) {
	bytesValue := make([]byte, 16)
	if _, err := rand.Read(bytesValue); err != nil {
		return "", err
	}
	bytesValue[6] = (bytesValue[6] & 0x0f) | 0x40
	bytesValue[8] = (bytesValue[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(bytesValue[0:4]),
		hex.EncodeToString(bytesValue[4:6]),
		hex.EncodeToString(bytesValue[6:8]),
		hex.EncodeToString(bytesValue[8:10]),
		hex.EncodeToString(bytesValue[10:16]),
	), nil
}
