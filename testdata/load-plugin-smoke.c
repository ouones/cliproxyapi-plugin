#define _POSIX_C_SOURCE 200809L

#include <dlfcn.h>
#include <pthread.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

typedef struct {
    void *ptr;
    size_t len;
} cliproxy_buffer;

typedef struct cliproxy_host_api cliproxy_host_api;
typedef struct cliproxy_plugin_api cliproxy_plugin_api;

typedef int (*cliproxy_host_call_fn)(void *, const char *, const uint8_t *, size_t, cliproxy_buffer *);
typedef void (*cliproxy_host_free_fn)(void *, size_t);
typedef int (*cliproxy_plugin_call_fn)(char *, uint8_t *, size_t, cliproxy_buffer *);
typedef void (*cliproxy_plugin_free_fn)(void *, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);
typedef int (*cliproxy_plugin_init_fn)(const cliproxy_host_api *, cliproxy_plugin_api *);

struct cliproxy_host_api {
    uint32_t abi_version;
    void *host_ctx;
    cliproxy_host_call_fn call;
    cliproxy_host_free_fn free_buffer;
};

struct cliproxy_plugin_api {
    uint32_t abi_version;
    cliproxy_plugin_call_fn call;
    cliproxy_plugin_free_fn free_buffer;
    cliproxy_plugin_shutdown_fn shutdown;
};

enum rpc_path {
    rpc_chat_non_stream,
    rpc_chat_stream,
    rpc_responses_non_stream,
    rpc_responses_stream,
    rpc_anthropic_non_stream,
    rpc_anthropic_stream,
    rpc_path_count
};

static const char *const rpc_path_names[rpc_path_count] = {
    "chat/non-stream",
    "chat/stream",
    "responses/non-stream",
    "responses/stream",
    "anthropic/non-stream",
    "anthropic/stream",
};

static unsigned int rpc_path_counts[rpc_path_count];

static const char fixture_non_stream_base64[] =
    "eyJ0eXBlIjoicmVhc29uaW5nLWRlbHRhIiwidGV4dCI6InRoaW5rIn0KeyJ0eXBlIjoidGV4dC1kZWx0YSIsInRleHQiOiJhbnN3ZXIifQp7InR5cGUiOiJ0b29sLWNhbGwiLCJ0b29sQ2FsbElkIjoiY2FsbF8xIiwidG9vbE5hbWUiOiJsb29rdXAiLCJpbnB1dCI6eyJxIjoieCJ9fQp7InR5cGUiOiJmaW5pc2giLCJmaW5pc2hSZWFzb24iOiJ0b29sLWNhbGxzIiwidG90YWxVc2FnZSI6eyJpbnB1dFRva2VucyI6OSwib3V0cHV0VG9rZW5zIjozLCJjYWNoZWRJbnB1dFRva2VucyI6MiwiaW5wdXRUb2tlbkRldGFpbHMiOnsiY2FjaGVXcml0ZVRva2VucyI6MSwibm9DYWNoZVRva2VucyI6N319fQo=";

static const char fixture_stream_base64[] =
    "eyJ0eXBlIjoicmVhc29uaW5nLWRlbHRhIiwidGV4dCI6InRoaW5rIn0KeyJ0eXBlIjoidGV4dC1kZWx0YSIsInRleHQiOiJhbnN3ZXIifQp7InR5cGUiOiJ0b29sLWNhbGwiLCJ0b29sQ2FsbElkIjoiY2FsbF8xIiwidG9vbE5hbWUiOiJsb29rdXAiLCJpbnB1dCI6eyJxIjoieCJ9fQp7InR5cGUiOiJmaW5pc2giLCJmaW5pc2hSZWFzb24iOiJzdG9wIiwidG90YWxVc2FnZSI6eyJpbnB1dFRva2VucyI6OSwib3V0cHV0VG9rZW5zIjozLCJjYWNoZWRJbnB1dFRva2VucyI6MiwiaW5wdXRUb2tlbkRldGFpbHMiOnsiY2FjaGVXcml0ZVRva2VucyI6MSwibm9DYWNoZVRva2VucyI6N319fQo=";

static const char config_base64[] = "YXBpLWtleTogdXNlcl9zbW9rZQo=";

static const char chat_non_payload_base64[] =
    "eyJtZXNzYWdlcyI6W3sicm9sZSI6InVzZXIiLCJjb250ZW50IjoiaGVsbG8ifV0sInRvb2xzIjpbeyJ0eXBlIjoiZnVuY3Rpb24iLCJmdW5jdGlvbiI6eyJuYW1lIjoibG9va3VwIiwicGFyYW1ldGVycyI6eyJ0eXBlIjoib2JqZWN0In19fV0sInN0cmVhbSI6ZmFsc2V9";
static const char chat_stream_payload_base64[] =
    "eyJtZXNzYWdlcyI6W3sicm9sZSI6InVzZXIiLCJjb250ZW50IjoiaGVsbG8ifV0sInRvb2xzIjpbeyJ0eXBlIjoiZnVuY3Rpb24iLCJmdW5jdGlvbiI6eyJuYW1lIjoibG9va3VwIiwicGFyYW1ldGVycyI6eyJ0eXBlIjoib2JqZWN0In19fV0sInN0cmVhbSI6dHJ1ZX0=";
static const char responses_non_payload_base64[] =
    "eyJtb2RlbCI6ImdwdC1zbW9rZSIsImluc3RydWN0aW9ucyI6InN5c3RlbSIsImlucHV0IjoiaGVsbG8iLCJ0b29scyI6W3sidHlwZSI6ImZ1bmN0aW9uIiwibmFtZSI6Imxvb2t1cCIsInBhcmFtZXRlcnMiOnsidHlwZSI6Im9iamVjdCJ9fV0sInN0cmVhbSI6ZmFsc2V9";
static const char responses_stream_payload_base64[] =
    "eyJtb2RlbCI6ImdwdC1zbW9rZSIsImluc3RydWN0aW9ucyI6InN5c3RlbSIsImlucHV0IjoiaGVsbG8iLCJ0b29scyI6W3sidHlwZSI6ImZ1bmN0aW9uIiwibmFtZSI6Imxvb2t1cCIsInBhcmFtZXRlcnMiOnsidHlwZSI6Im9iamVjdCJ9fV0sInN0cmVhbSI6dHJ1ZX0=";
static const char anthropic_non_payload_base64[] =
    "eyJtb2RlbCI6ImNsYXVkZS1zbW9rZSIsIm1heF90b2tlbnMiOjY0LCJzeXN0ZW0iOiJzeXN0ZW0iLCJtZXNzYWdlcyI6W3sicm9sZSI6InVzZXIiLCJjb250ZW50IjoiaGVsbG8ifV0sInRvb2xzIjpbeyJuYW1lIjoibG9va3VwIiwiaW5wdXRfc2NoZW1hIjp7InR5cGUiOiJvYmplY3QifX1dLCJ0aGlua2luZyI6eyJ0eXBlIjoiZW5hYmxlZCIsImJ1ZGdldF90b2tlbnMiOjEwMDB9LCJzdHJlYW0iOmZhbHNlfQ==";
static const char anthropic_stream_payload_base64[] =
    "eyJtb2RlbCI6ImNsYXVkZS1zbW9rZSIsIm1heF90b2tlbnMiOjY0LCJzeXN0ZW0iOiJzeXN0ZW0iLCJtZXNzYWdlcyI6W3sicm9sZSI6InVzZXIiLCJjb250ZW50IjoiaGVsbG8ifV0sInRvb2xzIjpbeyJuYW1lIjoibG9va3VwIiwiaW5wdXRfc2NoZW1hIjp7InR5cGUiOiJvYmplY3QifX1dLCJ0aGlua2luZyI6eyJ0eXBlIjoiZW5hYmxlZCIsImJ1ZGdldF90b2tlbnMiOjEwMDB9LCJzdHJlYW0iOnRydWV9";

typedef struct {
    const char *id;
    unsigned int reads;
    unsigned int closes;
} smoke_http_stream;

typedef struct {
    const char *id;
    unsigned int emits;
    unsigned int closes;
    int close_had_error;
    size_t output_len;
    char output[32768];
} smoke_rpc_stream;

typedef struct {
    pthread_mutex_t mutex;
    unsigned int host_http_do_calls;
    unsigned int host_http_do_stream_calls;
    unsigned int host_http_stream_read_calls;
    unsigned int host_http_stream_close_calls;
    unsigned int host_stream_emit_calls;
    unsigned int host_stream_close_calls;
    unsigned int host_log_calls;
    unsigned int invalid_callback_calls;
    unsigned int non_stream_generate_calls;
    unsigned int stream_generate_calls;
    unsigned int fingerprint_calls;
    unsigned int lifecycle_calls;
    smoke_http_stream http_streams[4];
    smoke_rpc_stream rpc_streams[4];
} smoke_host;

static int fail(const char *message) {
    fprintf(stderr, "%s\n", message);
    return 1;
}

static int bytes_contains(const uint8_t *value, size_t value_len, const char *needle) {
    size_t needle_len;

    if (value == NULL || needle == NULL) {
        return 0;
    }
    needle_len = strlen(needle);
    if (needle_len == 0 || value_len < needle_len) {
        return 0;
    }
    for (size_t offset = 0; offset <= value_len - needle_len; offset++) {
        if (memcmp(value + offset, needle, needle_len) == 0) {
            return 1;
        }
    }
    return 0;
}

static const uint8_t *find_bytes(const uint8_t *value, size_t value_len, const char *needle) {
    size_t needle_len;

    if (value == NULL || needle == NULL) {
        return NULL;
    }
    needle_len = strlen(needle);
    if (needle_len == 0 || value_len < needle_len) {
        return NULL;
    }
    for (size_t offset = 0; offset <= value_len - needle_len; offset++) {
        if (memcmp(value + offset, needle, needle_len) == 0) {
            return value + offset;
        }
    }
    return NULL;
}

static size_t copy_json_string(const uint8_t *value, size_t value_len, const char *key, char *output, size_t output_cap) {
    char prefix[64];
    const uint8_t *start;
    const uint8_t *end;
    size_t prefix_len;
    size_t string_len;

    if (output == NULL || output_cap == 0 || key == NULL ||
        snprintf(prefix, sizeof(prefix), "\"%s\":\"", key) < 0) {
        return 0;
    }
    prefix_len = strlen(prefix);
    start = find_bytes(value, value_len, prefix);
    if (start == NULL) {
        return 0;
    }
    start += prefix_len;
    end = start;
    while ((size_t)(end - value) < value_len && *end != '"') {
        end++;
    }
    if ((size_t)(end - value) == value_len) {
        return 0;
    }
    string_len = (size_t)(end - start);
    if (string_len >= output_cap) {
        string_len = output_cap - 1;
    }
    memcpy(output, start, string_len);
    output[string_len] = '\0';
    return string_len;
}

static size_t copy_json_array_string(const uint8_t *value, size_t value_len, const char *key, char *output, size_t output_cap) {
    char prefix[64];
    const uint8_t *start;
    const uint8_t *end;
    size_t prefix_len;
    size_t string_len;

    if (output == NULL || output_cap == 0 || key == NULL ||
        snprintf(prefix, sizeof(prefix), "\"%s\":[\"", key) < 0) {
        return 0;
    }
    prefix_len = strlen(prefix);
    start = find_bytes(value, value_len, prefix);
    if (start == NULL) {
        return 0;
    }
    start += prefix_len;
    end = start;
    while ((size_t)(end - value) < value_len && *end != '"') {
        end++;
    }
    if ((size_t)(end - value) == value_len) {
        return 0;
    }
    string_len = (size_t)(end - start);
    if (string_len >= output_cap) {
        string_len = output_cap - 1;
    }
    memcpy(output, start, string_len);
    output[string_len] = '\0';
    return string_len;
}

static int base64_value(char value) {
    if (value >= 'A' && value <= 'Z') {
        return value - 'A';
    }
    if (value >= 'a' && value <= 'z') {
        return value - 'a' + 26;
    }
    if (value >= '0' && value <= '9') {
        return value - '0' + 52;
    }
    if (value == '+') {
        return 62;
    }
    if (value == '/') {
        return 63;
    }
    return -1;
}

static size_t decode_base64(const char *encoded, uint8_t *decoded, size_t decoded_cap) {
    size_t encoded_len;
    size_t output_len = 0;
    unsigned int accumulator = 0;
    unsigned int bits = 0;

    if (encoded == NULL || decoded == NULL) {
        return 0;
    }
    encoded_len = strlen(encoded);
    for (size_t index = 0; index < encoded_len; index++) {
        int value = base64_value(encoded[index]);
        if (encoded[index] == '=') {
            break;
        }
        if (value < 0) {
            return 0;
        }
        accumulator = (accumulator << 6) | (unsigned int)value;
        bits += 6;
        if (bits >= 8) {
            bits -= 8;
            if (output_len >= decoded_cap) {
                return 0;
            }
            decoded[output_len++] = (uint8_t)((accumulator >> bits) & 0xffU);
            if (bits == 0) {
                accumulator = 0;
            } else {
                accumulator &= (1U << bits) - 1U;
            }
        }
    }
    return output_len;
}

static int write_response_bytes(cliproxy_buffer *response, const char *value, size_t value_len) {
    void *copy;

    if (response == NULL || value == NULL || value_len == 0) {
        return 1;
    }
    copy = malloc(value_len);
    if (copy == NULL) {
        return 1;
    }
    memcpy(copy, value, value_len);
    response->ptr = copy;
    response->len = value_len;
    return 0;
}

static int write_envelope_result(cliproxy_buffer *response, const char *result) {
    char *envelope;
    size_t result_len;
    size_t envelope_cap;
    int written;

    if (response == NULL || result == NULL) {
        return 1;
    }
    result_len = strlen(result);
    envelope_cap = result_len + 32;
    envelope = malloc(envelope_cap);
    if (envelope == NULL) {
        return 1;
    }
    written = snprintf(envelope, envelope_cap, "{\"ok\":true,\"result\":%s}", result);
    if (written < 0 || (size_t)written >= envelope_cap) {
        free(envelope);
        return 1;
    }
    response->ptr = envelope;
    response->len = (size_t)written;
    return 0;
}

static int write_envelope_error(cliproxy_buffer *response, const char *code, const char *message) {
    char envelope[512];
    int written;

    if (response == NULL || code == NULL || message == NULL) {
        return 1;
    }
    written = snprintf(envelope, sizeof(envelope), "{\"ok\":false,\"error\":{\"code\":\"%s\",\"message\":\"%s\"}}", code, message);
    if (written < 0 || (size_t)written >= sizeof(envelope)) {
        return 1;
    }
    return write_response_bytes(response, envelope, (size_t)written);
}

static int rpc_path_for_request(const char *method, const uint8_t *request, size_t request_len) {
    int stream;
    int protocol;

    if (method == NULL || request == NULL || request_len == 0 ||
        (strcmp(method, "executor.execute") != 0 && strcmp(method, "executor.execute_stream") != 0)) {
        return -1;
    }
    stream = strcmp(method, "executor.execute_stream") == 0;
    if (bytes_contains(request, request_len, "\"format\":\"chat-completions\"")) {
        protocol = 0;
    } else if (bytes_contains(request, request_len, "\"format\":\"responses\"") ||
               bytes_contains(request, request_len, "\"format\":\"openai-response\"")) {
        protocol = 1;
    } else if (bytes_contains(request, request_len, "\"format\":\"anthropic\"")) {
        protocol = 2;
    } else {
        return -1;
    }
    return protocol * 2 + stream;
}

static int call_rpc(const cliproxy_plugin_api *plugin, const char *method, const uint8_t *request,
                    size_t request_len, cliproxy_buffer *response) {
    int path = rpc_path_for_request(method, request, request_len);
    if (path >= 0) {
        rpc_path_counts[path]++;
    }
    return plugin->call((char *)method, (uint8_t *)request, request_len, response);
}

static int require_rpc_paths(void) {
    int failed = 0;

    for (int path = 0; path < rpc_path_count; path++) {
        fprintf(stderr, "[native-smoke] %s count=%u\n", rpc_path_names[path], rpc_path_counts[path]);
        if (rpc_path_counts[path] != 1) {
            failed = 1;
        }
    }
    return failed ? fail("native smoke did not execute each required RPC path exactly once") : 0;
}

static int http_stream_index(const char *stream_id) {
    static const char *const ids[] = {"http-chat", "http-responses", "http-anthropic", "http-cancel"};

    if (stream_id == NULL) {
        return -1;
    }
    for (int index = 0; index < 4; index++) {
        if (strcmp(stream_id, ids[index]) == 0) {
            return index;
        }
    }
    return -1;
}

static int rpc_stream_index(const char *stream_id) {
    static const char *const ids[] = {"rpc-chat-stream", "rpc-responses-stream", "rpc-anthropic-stream", "rpc-cancel"};

    if (stream_id == NULL) {
        return -1;
    }
    for (int index = 0; index < 4; index++) {
        if (strcmp(stream_id, ids[index]) == 0) {
            return index;
        }
    }
    return -1;
}

static void smoke_host_init(smoke_host *host) {
    memset(host, 0, sizeof(*host));
    pthread_mutex_init(&host->mutex, NULL);
    host->http_streams[0].id = "http-chat";
    host->http_streams[1].id = "http-responses";
    host->http_streams[2].id = "http-anthropic";
    host->http_streams[3].id = "http-cancel";
    host->rpc_streams[0].id = "rpc-chat-stream";
    host->rpc_streams[1].id = "rpc-responses-stream";
    host->rpc_streams[2].id = "rpc-anthropic-stream";
    host->rpc_streams[3].id = "rpc-cancel";
}

static void smoke_host_destroy(smoke_host *host) {
    pthread_mutex_destroy(&host->mutex);
}

static int request_has_header(const uint8_t *request, size_t request_len, const char *name, const char *value) {
    char fragment[256];
    int written;

    written = snprintf(fragment, sizeof(fragment), "\"%s\":[\"%s\"]", name, value);
    return written > 0 && (size_t)written < sizeof(fragment) && bytes_contains(request, request_len, fragment);
}

static int is_hex_digit(char value) {
    return (value >= '0' && value <= '9') || (value >= 'a' && value <= 'f') ||
           (value >= 'A' && value <= 'F');
}

static int has_valid_traceparent(const uint8_t *request, size_t request_len) {
    char traceparent[64];
    size_t length = copy_json_array_string(request, request_len, "traceparent", traceparent, sizeof(traceparent));

    if (length != 55 || traceparent[0] != '0' || traceparent[1] != '0' || traceparent[2] != '-' ||
        traceparent[35] != '-' || traceparent[52] != '-' || traceparent[53] != '0' || traceparent[54] != '1') {
        return 0;
    }
    for (size_t index = 3; index < 35; index++) {
        if (!is_hex_digit(traceparent[index])) {
            return 0;
        }
    }
    for (size_t index = 36; index < 52; index++) {
        if (!is_hex_digit(traceparent[index])) {
            return 0;
        }
    }
    return 1;
}

static size_t decode_request_body(const uint8_t *request, size_t request_len, uint8_t *body, size_t body_cap) {
    char encoded[65536];
    size_t encoded_len = copy_json_string(request, request_len, "body", encoded, sizeof(encoded));

    if (encoded_len == 0) {
        return 0;
    }
    return decode_base64(encoded, body, body_cap);
}

static int has_valid_date(const uint8_t *body, size_t body_len) {
    char date[32];
    size_t length = copy_json_string(body, body_len, "date", date, sizeof(date));

    if (length != 10 || date[4] != '-' || date[7] != '-') {
        return 0;
    }
    for (size_t index = 0; index < length; index++) {
        if (index == 4 || index == 7) {
            continue;
        }
        if (date[index] < '0' || date[index] > '9') {
            return 0;
        }
    }
    return 1;
}

static int body_has_fragments(const uint8_t *body, size_t body_len, const char *const *fragments,
                              size_t fragment_count) {
    for (size_t index = 0; index < fragment_count; index++) {
        if (!bytes_contains(body, body_len, fragments[index])) {
            return 0;
        }
    }
    return 1;
}

static int validate_generate_request(const uint8_t *request, size_t request_len) {
    static const char *const common_fragments[] = {
        "\"workingDir\":\"C:\\\\Users\\\\dev\\\\projects\\\\app\"",
        "\"environment\":\"win32\"",
        "\"isGitRepo\":false",
        "\"currentBranch\":\"\"",
        "\"mainBranch\":\"\"",
        "\"gitStatus\":\"\"",
        "\"recentCommits\":[]",
        "\"memory\":null",
        "\"taste\":null",
        "\"skills\":null",
        "\"permissionMode\":\"standard\"",
        "\"threadId\":\"11111111-1111-4111-8111-111111111111\"",
        "\"mode\":\"agent\"",
    };
    static const char *const chat_fragments[] = {
        "\"model\":\"smoke-model\"",
        "\"text\":\"hello\"",
        "\"role\":\"user\"",
        "\"max_tokens\":64000",
        "\"stream\":true",
        "\"system\":[{\"text\":\" \",\"type\":\"text\"}]",
        "\"input_schema\":{\"type\":\"object\"}",
        "\"name\":\"lookup\"",
    };
    static const char *const responses_fragments[] = {
        "\"model\":\"gpt-smoke\"",
        "\"text\":\"hello\"",
        "\"text\":\"system\"",
        "\"role\":\"user\"",
        "\"max_tokens\":64000",
        "\"stream\":true",
        "\"input_schema\":{\"type\":\"object\"}",
        "\"name\":\"lookup\"",
    };
    static const char *const anthropic_fragments[] = {
        "\"model\":\"claude-smoke\"",
        "\"text\":\"hello\"",
        "\"text\":\"system\"",
        "\"role\":\"user\"",
        "\"max_tokens\":64",
        "\"stream\":true",
        "\"reasoning_effort\":\"low\"",
        "\"input_schema\":{\"type\":\"object\"}",
        "\"name\":\"lookup\"",
    };
    uint8_t body[32768];
    size_t body_len;
    int common_match;

    if (!bytes_contains(request, request_len, "\"method\":\"POST\"") ||
        !bytes_contains(request, request_len, "\"url\":\"https://api.commandcode.ai/alpha/generate\"") ||
        !bytes_contains(request, request_len, "\"host_callback_id\":\"callback-smoke\"") ||
        !request_has_header(request, request_len, "Content-Type", "application/json") ||
        !request_has_header(request, request_len, "User-Agent", "cli") ||
        !request_has_header(request, request_len, "x-command-code-version", "1.58.0") ||
        !request_has_header(request, request_len, "x-cli-environment", "production") ||
        !request_has_header(request, request_len, "x-project-slug", "c-users-dev-projects-app") ||
        !request_has_header(request, request_len, "x-taste-learning", "false") ||
        !request_has_header(request, request_len, "x-session-id", "11111111-1111-4111-8111-111111111111") ||
        !request_has_header(request, request_len, "Authorization", "Bearer user_smoke") ||
        !has_valid_traceparent(request, request_len)) {
        return 0;
    }

    body_len = decode_request_body(request, request_len, body, sizeof(body));
    common_match = body_has_fragments(body, body_len, common_fragments, sizeof(common_fragments) / sizeof(common_fragments[0]));
    if (body_len == 0 || !has_valid_date(body, body_len) || !common_match) {
        return 0;
    }
    if (bytes_contains(body, body_len, "\"model\":\"smoke-model\"")) {
        return body_has_fragments(body, body_len, chat_fragments, sizeof(chat_fragments) / sizeof(chat_fragments[0]));
    }
    if (bytes_contains(body, body_len, "\"model\":\"gpt-smoke\"")) {
        return body_has_fragments(body, body_len, responses_fragments, sizeof(responses_fragments) / sizeof(responses_fragments[0]));
    }
    if (bytes_contains(body, body_len, "\"model\":\"claude-smoke\"")) {
        return body_has_fragments(body, body_len, anthropic_fragments, sizeof(anthropic_fragments) / sizeof(anthropic_fragments[0]));
    }
    return 0;
}

static int validate_identity_headers(const uint8_t *request, size_t request_len, const char *url) {
    return bytes_contains(request, request_len, "\"method\":\"POST\"") &&
           bytes_contains(request, request_len, url) &&
           bytes_contains(request, request_len, "\"host_callback_id\":\"callback-smoke\"") &&
           request_has_header(request, request_len, "Content-Type", "application/json") &&
           request_has_header(request, request_len, "x-cli-environment", "production") &&
           request_has_header(request, request_len, "Authorization", "Bearer user_smoke") &&
           request_has_header(request, request_len, "x-command-code-version", "1.58.0");
}

static int validate_fingerprint_request(const uint8_t *request, size_t request_len) {
    static const char *const fragments[] = {
        "\"thumbmark\":\"db23f4e55fec7777ecb5f1a180ef7208cdc273ca7873a017b726524c914d60e8\"",
        "\"machineIdHash\":\"8ec097d2e402b41b90aad6893ac3bc5b911207e2dc430337274c17ba3e3328cc\"",
        "\"macHashes\":[\"43d3b7e683f42b5f4389898b2c7a6de7c1075847731ae589ad1e58eff5af5069\",\"9a193d12fddffadacdb367cfd1de46f40b5b359ea90c58d47b6f36f8c8b19769\",\"813e6a5bfc7536a5c0b673d8e3a76e387d9e4b733656dd0874e35161a8305e76\"]",
        "\"osUserHash\":\"845392654d16216abd349a7bf1911584c4e74091483182f5fb6745e24d8bcabf\"",
        "\"hostnameHash\":\"bd076f0010b35dcbfe6ddb3d3ba01625382d7772ba9836a55c073f784b25c5ad\"",
        "\"gitEmailHash\":\"9dfd634a7b2600c3220b9d901fe63654cb73175a8b719c8d775c85bc653da0c1\"",
        "\"platform\":\"win32\"",
        "\"arch\":\"x64\"",
        "\"osRelease\":\"10.0.22631\"",
        "\"cpuModel\":\"Intel(R) Core(TM) Ultra 7 155H\"",
        "\"cpuCount\":16",
        "\"memGiB\":8",
        "\"isContainer\":false",
        "\"timezone\":\"Asia/Tokyo\"",
        "\"runtime\":\"cli\"",
        "\"collectorVersion\":1",
    };
    uint8_t body[8192];
    size_t body_len;

    if (!validate_identity_headers(request, request_len, "\"url\":\"https://api.commandcode.ai/alpha/fingerprint/record\"")) {
        return 0;
    }
    body_len = decode_request_body(request, request_len, body, sizeof(body));
    return body_len > 0 && body_has_fragments(body, body_len, fragments, sizeof(fragments) / sizeof(fragments[0]));
}

static int validate_lifecycle_request(const uint8_t *request, size_t request_len) {
    uint8_t body[4096];
    char session_id[64];
    size_t body_len;
    size_t session_len;

    if (!validate_identity_headers(request, request_len, "\"url\":\"https://api.commandcode.ai/alpha/lifecycle-events\"")) {
        return 0;
    }
    body_len = decode_request_body(request, request_len, body, sizeof(body));
    session_len = copy_json_string(body, body_len, "sessionId", session_id, sizeof(session_id));
    if (body_len == 0 || session_len != 21 || strncmp(session_id, "sess_", 5) != 0 ||
        !bytes_contains(body, body_len, "\"eventType\":\"cli_session_exists\"") ||
        !bytes_contains(body, body_len, "\"cliVersion\":\"1.58.0\"") ||
        !bytes_contains(body, body_len, "\"mode\":\"interactive\"") ||
        !bytes_contains(body, body_len, "\"os\":\"win32-x64\"")) {
        return 0;
    }
    for (size_t index = 5; index < session_len; index++) {
        if (!is_hex_digit(session_id[index])) {
            return 0;
        }
    }
    return 1;
}

static int validate_registry_request(const uint8_t *request, size_t request_len) {
    return bytes_contains(request, request_len, "\"method\":\"GET\"") &&
           bytes_contains(request, request_len, "\"url\":\"https://registry.npmjs.org/command-code/latest\"") &&
           request_has_header(request, request_len, "accept", "application/json");
}

static int host_http_do(smoke_host *host, const uint8_t *request, size_t request_len, cliproxy_buffer *response) {
    unsigned int generate_index;

    pthread_mutex_lock(&host->mutex);
    host->host_http_do_calls++;
    pthread_mutex_unlock(&host->mutex);

    if (bytes_contains(request, request_len, "/alpha/generate")) {
        if (!validate_generate_request(request, request_len)) {
            return write_envelope_error(response, "invalid_generate", "generate wire contract mismatch");
        }
        pthread_mutex_lock(&host->mutex);
        generate_index = host->non_stream_generate_calls++;
        pthread_mutex_unlock(&host->mutex);
        if (generate_index < 3) {
            char body[2048];
            snprintf(body, sizeof(body), "{\"status_code\":200,\"headers\":{\"content-type\":[\"application/json\"]},\"body\":\"%s\"}", fixture_non_stream_base64);
            return write_envelope_result(response, body);
        }
        if (generate_index == 3) {
            return write_envelope_result(response, "{\"status_code\":429}");
        }
        return write_envelope_error(response, "unexpected_generate", "too many non-stream fixture requests");
    }
    if (bytes_contains(request, request_len, "/alpha/fingerprint/record")) {
        if (!validate_fingerprint_request(request, request_len)) {
            return write_envelope_error(response, "invalid_fingerprint", "fingerprint wire contract mismatch");
        }
        pthread_mutex_lock(&host->mutex);
        host->fingerprint_calls++;
        pthread_mutex_unlock(&host->mutex);
        return write_envelope_result(response, "{\"status_code\":200}");
    }
    if (bytes_contains(request, request_len, "/alpha/lifecycle-events")) {
        if (!validate_lifecycle_request(request, request_len)) {
            return write_envelope_error(response, "invalid_lifecycle", "lifecycle wire contract mismatch");
        }
        pthread_mutex_lock(&host->mutex);
        host->lifecycle_calls++;
        pthread_mutex_unlock(&host->mutex);
        return write_envelope_result(response, "{\"status_code\":200}");
    }
    if (bytes_contains(request, request_len, "registry.npmjs.org")) {
        if (!validate_registry_request(request, request_len)) {
            return write_envelope_error(response, "invalid_registry", "registry wire contract mismatch");
        }
        return write_envelope_result(response, "{\"status_code\":200,\"body\":\"eyJ2ZXJzaW9uIjoiMS41OC4wIn0=\"}");
    }
    return write_envelope_error(response, "unsupported_http_request", "unexpected HTTP fixture request");
}

static int require_wire_rejection(smoke_host *host, const char *label, const char *request) {
    cliproxy_buffer response = {0};
    int rejected;

    if (host_http_do(host, (const uint8_t *)request, strlen(request), &response) != 0) {
        return fail("wire rejection fixture host call failed");
    }
    rejected = response.ptr != NULL && bytes_contains(response.ptr, response.len, "\"ok\":false");
    if (response.ptr != NULL) {
        free(response.ptr);
    }
    if (!rejected) {
        fprintf(stderr, "[native-smoke] %s was accepted by the host fixture\n", label);
        return fail("native smoke host accepted an invalid wire fixture");
    }
    return 0;
}

static int run_wire_rejection_fixtures(smoke_host *host) {
    static const char generate_with_wrong_headers[] =
        "{\"method\":\"POST\",\"url\":\"https://api.commandcode.ai/alpha/generate\","
        "\"headers\":{\"content-type\":[\"application/json\"],\"x-command-code-version\":[\"1.57.0\"]},"
        "\"body\":\"e30=\"}";
    static const char empty_fingerprint[] =
        "{\"method\":\"POST\",\"url\":\"https://api.commandcode.ai/alpha/fingerprint/record\","
        "\"headers\":{\"x-command-code-version\":[\"1.58.0\"]},\"body\":\"e30=\"}";
    static const char wrong_lifecycle_event[] =
        "{\"method\":\"POST\",\"url\":\"https://api.commandcode.ai/alpha/lifecycle-events\","
        "\"headers\":{\"x-command-code-version\":[\"1.58.0\"]},"
        "\"body\":\"eyJldmVudFR5cGUiOiJ3cm9uZyJ9\"}";

    return require_wire_rejection(host, "generate/wrong-headers", generate_with_wrong_headers) ||
           require_wire_rejection(host, "fingerprint/empty-payload", empty_fingerprint) ||
           require_wire_rejection(host, "lifecycle/wrong-event", wrong_lifecycle_event);
}

static int host_http_do_stream(smoke_host *host, const uint8_t *request, size_t request_len, cliproxy_buffer *response) {
    unsigned int stream_index;
    char result[256];
    const char *stream_id;

    if (!bytes_contains(request, request_len, "/alpha/generate") || !validate_generate_request(request, request_len)) {
        return write_envelope_error(response, "unexpected_http_stream", "stream fixture URL is not Command Code generate");
    }
    pthread_mutex_lock(&host->mutex);
    host->host_http_do_stream_calls++;
    stream_index = host->stream_generate_calls++;
    pthread_mutex_unlock(&host->mutex);
    if (stream_index >= 4) {
        return write_envelope_error(response, "unexpected_stream", "too many stream fixture requests");
    }
    stream_id = host->http_streams[stream_index].id;
    snprintf(result, sizeof(result), "{\"status_code\":200,\"headers\":{\"content-type\":[\"text/event-stream\"]},\"stream_id\":\"%s\"}", stream_id);
    return write_envelope_result(response, result);
}

static int host_http_stream_read(smoke_host *host, const uint8_t *request, size_t request_len, cliproxy_buffer *response) {
    char stream_id[64];
    unsigned int read_index;
    int stream_index;
    char result[4096];

    if (copy_json_string(request, request_len, "stream_id", stream_id, sizeof(stream_id)) == 0) {
        return write_envelope_error(response, "invalid_stream", "stream_id is required");
    }
    stream_index = http_stream_index(stream_id);
    if (stream_index < 0) {
        return write_envelope_error(response, "unknown_stream", "unknown host HTTP stream");
    }
    pthread_mutex_lock(&host->mutex);
    host->host_http_stream_read_calls++;
    read_index = host->http_streams[stream_index].reads++;
    pthread_mutex_unlock(&host->mutex);
    if (read_index == 0) {
        snprintf(result, sizeof(result), "{\"payload\":\"%s\"}", fixture_stream_base64);
    } else {
        snprintf(result, sizeof(result), "{\"done\":true}");
    }
    return write_envelope_result(response, result);
}

static int host_http_stream_close(smoke_host *host, const uint8_t *request, size_t request_len, cliproxy_buffer *response) {
    char stream_id[64];
    int stream_index;

    if (copy_json_string(request, request_len, "stream_id", stream_id, sizeof(stream_id)) == 0) {
        return write_envelope_error(response, "invalid_stream", "stream_id is required");
    }
    stream_index = http_stream_index(stream_id);
    if (stream_index < 0) {
        return write_envelope_error(response, "unknown_stream", "unknown host HTTP stream");
    }
    pthread_mutex_lock(&host->mutex);
    host->host_http_stream_close_calls++;
    host->http_streams[stream_index].closes++;
    pthread_mutex_unlock(&host->mutex);
    return write_envelope_result(response, "{}");
}

static int host_stream_emit(smoke_host *host, const uint8_t *request, size_t request_len, cliproxy_buffer *response) {
    char stream_id[64];
    char encoded_payload[8192];
    uint8_t decoded_payload[8192];
    size_t encoded_len;
    size_t decoded_len;
    int stream_index;

    if (copy_json_string(request, request_len, "stream_id", stream_id, sizeof(stream_id)) == 0) {
        return write_envelope_error(response, "invalid_stream", "stream_id is required");
    }
    stream_index = rpc_stream_index(stream_id);
    if (stream_index < 0) {
        return write_envelope_error(response, "unknown_stream", "unknown RPC stream");
    }
    encoded_len = copy_json_string(request, request_len, "payload", encoded_payload, sizeof(encoded_payload));
    decoded_len = encoded_len == 0 ? 0 : decode_base64(encoded_payload, decoded_payload, sizeof(decoded_payload));
    pthread_mutex_lock(&host->mutex);
    host->host_stream_emit_calls++;
    host->rpc_streams[stream_index].emits++;
    if (decoded_len > 0 && host->rpc_streams[stream_index].output_len + decoded_len < sizeof(host->rpc_streams[stream_index].output)) {
        memcpy(host->rpc_streams[stream_index].output + host->rpc_streams[stream_index].output_len, decoded_payload, decoded_len);
        host->rpc_streams[stream_index].output_len += decoded_len;
        host->rpc_streams[stream_index].output[host->rpc_streams[stream_index].output_len] = '\0';
    }
    pthread_mutex_unlock(&host->mutex);
    if (stream_index == 3) {
        return write_envelope_error(response, "host_stream_rejected", "smoke host rejected the cancellation fixture");
    }
    return write_envelope_result(response, "{}");
}

static int host_stream_close(smoke_host *host, const uint8_t *request, size_t request_len, cliproxy_buffer *response) {
    char stream_id[64];
    char error_message[256] = {0};
    int stream_index;

    if (copy_json_string(request, request_len, "stream_id", stream_id, sizeof(stream_id)) == 0) {
        return write_envelope_error(response, "invalid_stream", "stream_id is required");
    }
    stream_index = rpc_stream_index(stream_id);
    if (stream_index < 0) {
        return write_envelope_error(response, "unknown_stream", "unknown RPC stream");
    }
    copy_json_string(request, request_len, "error", error_message, sizeof(error_message));
    pthread_mutex_lock(&host->mutex);
    host->host_stream_close_calls++;
    host->rpc_streams[stream_index].closes++;
    if (error_message[0] != '\0') {
        host->rpc_streams[stream_index].close_had_error = 1;
    }
    pthread_mutex_unlock(&host->mutex);
    return write_envelope_result(response, "{}");
}

static int smoke_host_call(void *context, const char *method, const uint8_t *request, size_t request_len, cliproxy_buffer *response) {
    smoke_host *host = context;

    if (response == NULL) {
        return 1;
    }
    response->ptr = NULL;
    response->len = 0;
    if (host == NULL || method == NULL) {
        return write_envelope_error(response, "invalid_host_call", "host callback arguments are invalid");
    }
    if (strcmp(method, "host.http.do") == 0) {
        return host_http_do(host, request, request_len, response);
    }
    if (strcmp(method, "host.http.do_stream") == 0) {
        return host_http_do_stream(host, request, request_len, response);
    }
    if (strcmp(method, "host.http.stream_read") == 0) {
        return host_http_stream_read(host, request, request_len, response);
    }
    if (strcmp(method, "host.http.stream_close") == 0) {
        return host_http_stream_close(host, request, request_len, response);
    }
    if (strcmp(method, "host.stream.emit") == 0) {
        return host_stream_emit(host, request, request_len, response);
    }
    if (strcmp(method, "host.stream.close") == 0) {
        return host_stream_close(host, request, request_len, response);
    }
    if (strcmp(method, "host.log") == 0) {
        pthread_mutex_lock(&host->mutex);
        host->host_log_calls++;
        pthread_mutex_unlock(&host->mutex);
        return write_envelope_result(response, "{}");
    }
    pthread_mutex_lock(&host->mutex);
    host->invalid_callback_calls++;
    pthread_mutex_unlock(&host->mutex);
    return write_envelope_error(response, "unsupported_host_callback", "unexpected host callback method");
}

static void smoke_host_free(void *value, size_t value_len) {
    (void)value_len;
    free(value);
}

static int invoke_plugin(const cliproxy_plugin_api *plugin, const char *method, const char *request, cliproxy_buffer *response) {
    size_t request_len = request == NULL ? 0 : strlen(request);
    return plugin->call((char *)method, (uint8_t *)request, request_len, response);
}

static int response_has_markers(const char *label, const cliproxy_buffer *response, const char *const *markers, size_t marker_count) {
    char encoded_payload[65536];
    uint8_t decoded_payload[65536];
    const uint8_t *marker_value;
    size_t marker_value_len;

    if (response == NULL || response->ptr == NULL || response->len == 0 ||
        !bytes_contains(response->ptr, response->len, "\"ok\":true")) {
        fprintf(stderr, "[native-smoke] %s did not return a successful RPC envelope\n", label);
        return 1;
    }
    marker_value = response->ptr;
    marker_value_len = response->len;
    if (marker_count > 0) {
        size_t encoded_len = copy_json_string(response->ptr, response->len, "Payload", encoded_payload, sizeof(encoded_payload));
        if (encoded_len == 0) {
            encoded_len = copy_json_string(response->ptr, response->len, "payload", encoded_payload, sizeof(encoded_payload));
        }
        marker_value_len = encoded_len == 0 ? 0 : decode_base64(encoded_payload, decoded_payload, sizeof(decoded_payload));
        if (marker_value_len == 0) {
            fprintf(stderr, "[native-smoke] %s successful RPC envelope did not contain a decodable result payload\n", label);
            return 1;
        }
        marker_value = decoded_payload;
    }
    for (size_t index = 0; index < marker_count; index++) {
        if (!bytes_contains(marker_value, marker_value_len, markers[index])) {
            fprintf(stderr, "[native-smoke] %s result is missing marker %s\n", label, markers[index]);
            return 1;
        }
    }
    return 0;
}

static int build_executor_request(char *request, size_t request_cap, const char *model, const char *format,
                                  int stream, const char *payload_base64, const char *stream_id) {
    int written;

    if (stream_id == NULL) {
        written = snprintf(request, request_cap,
                           "{\"model\":\"%s\",\"format\":\"%s\",\"stream\":%s,\"payload\":\"%s\",\"headers\":{\"x-session-id\":[\"11111111-1111-4111-8111-111111111111\"]},\"host_callback_id\":\"callback-smoke\"}",
                           model, format, stream ? "true" : "false", payload_base64);
    } else {
        written = snprintf(request, request_cap,
                           "{\"model\":\"%s\",\"format\":\"%s\",\"stream\":%s,\"payload\":\"%s\",\"headers\":{\"x-session-id\":[\"11111111-1111-4111-8111-111111111111\"]},\"stream_id\":\"%s\",\"host_callback_id\":\"callback-smoke\"}",
                           model, format, stream ? "true" : "false", payload_base64, stream_id);
    }
    return written < 0 || (size_t)written >= request_cap;
}

static int wait_for_stream(smoke_host *host, int stream_index, int require_error) {
    unsigned int emits;
    unsigned int closes;
    unsigned int http_closes;
    int close_had_error;
    struct timespec pause = {0, 10000000L};

    for (int attempt = 0; attempt < 500; attempt++) {
        pthread_mutex_lock(&host->mutex);
        emits = host->rpc_streams[stream_index].emits;
        closes = host->rpc_streams[stream_index].closes;
        close_had_error = host->rpc_streams[stream_index].close_had_error;
        http_closes = host->http_streams[stream_index].closes;
        pthread_mutex_unlock(&host->mutex);
        if (closes > 0 && http_closes > 0) {
            fprintf(stderr, "[native-smoke] %s emit=%u close=%u host_http_close=%u\n",
                    host->rpc_streams[stream_index].id, emits, closes, http_closes);
            if (emits == 0 || closes != 1 || http_closes != 1 || close_had_error != require_error) {
                return fail("native smoke stream bridge counts or terminal error state are invalid");
            }
            return 0;
        }
        nanosleep(&pause, NULL);
    }
    fprintf(stderr, "[native-smoke] timed out waiting for %s stream close\n", host->rpc_streams[stream_index].id);
    return 1;
}

static int assert_stream_markers(smoke_host *host, int stream_index, const char *const *markers, size_t marker_count) {
    char output[32768];
    size_t output_len;

    pthread_mutex_lock(&host->mutex);
    output_len = host->rpc_streams[stream_index].output_len;
    if (output_len >= sizeof(output)) {
        output_len = sizeof(output) - 1;
    }
    memcpy(output, host->rpc_streams[stream_index].output, output_len);
    pthread_mutex_unlock(&host->mutex);
    output[output_len] = '\0';
    for (size_t index = 0; index < marker_count; index++) {
        if (!strstr(output, markers[index])) {
            fprintf(stderr, "[native-smoke] %s stream output is missing marker %s\n", host->rpc_streams[stream_index].id, markers[index]);
            return 1;
        }
    }
    return 0;
}

static int run_required_path(const cliproxy_plugin_api *plugin, smoke_host *host, const char *label, const char *method,
                             const char *model, const char *format, int stream, const char *payload_base64,
                             const char *stream_id, const char *const *markers, size_t marker_count) {
    char request[8192];
    cliproxy_buffer response = {0};
    int stream_index;
    int rc;
    int failed = 0;

    fprintf(stderr, "[native-smoke] execute %s via %s\n", label, method);
    if (build_executor_request(request, sizeof(request), model, format, stream, payload_base64, stream_id)) {
        return fail("could not build executor smoke request");
    }
    rc = call_rpc(plugin, method, (const uint8_t *)request, strlen(request), &response);
    if (rc != 0) {
        fprintf(stderr, "[native-smoke] %s RPC returned %d\n", label, rc);
        failed = 1;
    } else if (response_has_markers(label, &response, stream ? NULL : markers, stream ? 0 : marker_count)) {
        failed = 1;
    }
    if (response.ptr != NULL) {
        plugin->free_buffer(response.ptr, response.len);
    }
    if (stream) {
        stream_index = rpc_stream_index(stream_id);
        if (stream_index < 0 || wait_for_stream(host, stream_index, 0)) {
            failed = 1;
        } else if (assert_stream_markers(host, stream_index, markers, marker_count)) {
            failed = 1;
        }
    }
    return failed;
}

static int run_error_fixture(const cliproxy_plugin_api *plugin) {
    char request[8192];
    cliproxy_buffer response = {0};
    int rc;

    fprintf(stderr, "[native-smoke] execute error/non-stream via executor.execute\n");
    if (build_executor_request(request, sizeof(request), "smoke-model", "chat-completions", 0, chat_non_payload_base64, NULL)) {
        return fail("could not build error fixture request");
    }
    rc = invoke_plugin(plugin, "executor.execute", request, &response);
    if (rc == 0 || response.ptr == NULL || !bytes_contains(response.ptr, response.len, "\"ok\":false") ||
        !bytes_contains(response.ptr, response.len, "rate_limit_error") ||
        !bytes_contains(response.ptr, response.len, "\"http_status\":429")) {
        if (response.ptr != NULL) {
            plugin->free_buffer(response.ptr, response.len);
        }
        return fail("non-stream error fixture did not produce a rate-limit error envelope");
    }
    plugin->free_buffer(response.ptr, response.len);
    return 0;
}

static int run_cancellation_fixture(const cliproxy_plugin_api *plugin, smoke_host *host) {
    char request[8192];
    cliproxy_buffer response = {0};
    int rc;

    fprintf(stderr, "[native-smoke] execute cancellation/stream via executor.execute_stream\n");
    if (build_executor_request(request, sizeof(request), "smoke-model", "chat-completions", 1, chat_stream_payload_base64, "rpc-cancel")) {
        return fail("could not build cancellation fixture request");
    }
    rc = invoke_plugin(plugin, "executor.execute_stream", request, &response);
    if (rc != 0 || response.ptr == NULL || !bytes_contains(response.ptr, response.len, "\"ok\":true")) {
        if (response.ptr != NULL) {
            plugin->free_buffer(response.ptr, response.len);
        }
        return fail("cancellation fixture did not start through the stream RPC envelope");
    }
    plugin->free_buffer(response.ptr, response.len);
    return wait_for_stream(host, 3, 1);
}

static int validate_host(smoke_host *host) {
    unsigned int host_http_do_calls;
    unsigned int host_http_do_stream_calls;
    unsigned int host_http_stream_read_calls;
    unsigned int host_http_stream_close_calls;
    unsigned int host_stream_emit_calls;
    unsigned int host_stream_close_calls;
    unsigned int host_log_calls;
    unsigned int invalid_callback_calls;
    unsigned int non_stream_generate_calls;
    unsigned int stream_generate_calls;
    unsigned int fingerprint_calls;
    unsigned int lifecycle_calls;
    struct timespec pause = {0, 10000000L};

    for (int attempt = 0; attempt < 500; attempt++) {
        pthread_mutex_lock(&host->mutex);
        host_http_do_calls = host->host_http_do_calls;
        host_log_calls = host->host_log_calls;
        pthread_mutex_unlock(&host->mutex);
        if (host_http_do_calls >= 7 && host_log_calls > 0) {
            break;
        }
        nanosleep(&pause, NULL);
    }

    pthread_mutex_lock(&host->mutex);
    host_http_do_calls = host->host_http_do_calls;
    host_http_do_stream_calls = host->host_http_do_stream_calls;
    host_http_stream_read_calls = host->host_http_stream_read_calls;
    host_http_stream_close_calls = host->host_http_stream_close_calls;
    host_stream_emit_calls = host->host_stream_emit_calls;
    host_stream_close_calls = host->host_stream_close_calls;
    host_log_calls = host->host_log_calls;
    invalid_callback_calls = host->invalid_callback_calls;
    non_stream_generate_calls = host->non_stream_generate_calls;
    stream_generate_calls = host->stream_generate_calls;
    fingerprint_calls = host->fingerprint_calls;
    lifecycle_calls = host->lifecycle_calls;
    pthread_mutex_unlock(&host->mutex);

    fprintf(stderr, "[native-smoke] host callbacks http.do=%u http.do_stream=%u http.stream_read=%u http.stream_close=%u stream.emit=%u stream.close=%u log=%u invalid=%u fingerprint=%u lifecycle=%u\n",
            host_http_do_calls, host_http_do_stream_calls, host_http_stream_read_calls, host_http_stream_close_calls,
            host_stream_emit_calls, host_stream_close_calls, host_log_calls, invalid_callback_calls,
            fingerprint_calls, lifecycle_calls);
    if (host_http_do_stream_calls != 4 || host_http_stream_close_calls != 4 || host_stream_close_calls != 4 ||
        host_http_stream_read_calls < 7 || host_http_do_calls < 7 || host_log_calls == 0 || invalid_callback_calls != 0 ||
        non_stream_generate_calls != 4 || stream_generate_calls != 4 || fingerprint_calls != 1 || lifecycle_calls != 1) {
        return fail("native smoke host callback fixture counts are incomplete");
    }
    return 0;
}

int main(int argc, char **argv) {
    void *handle = NULL;
    cliproxy_plugin_init_fn init;
    const char *symbol_error;
    cliproxy_plugin_api plugin = {0};
    cliproxy_host_api host_api;
    smoke_host host;
    cliproxy_buffer response = {0};
    int failed = 0;
    char register_method[] = "plugin.register";
    char register_request[128];
    char identifier_method[] = "executor.identifier";
    static const char *const chat_non_markers[] = {
        "\"object\":\"chat.completion\"", "\"reasoning_content\":\"think\"", "\"tool_calls\"",
        "\"finish_reason\":\"tool_calls\"", "\"total_tokens\":12", "\"cached_tokens\":2",
    };
    static const char *const chat_stream_markers[] = {
        "reasoning_content", "\"object\":\"chat.completion.chunk\"", "\"finish_reason\":\"stop\"", "\"total_tokens\":12",
    };
    static const char *const responses_non_markers[] = {
        "\"object\":\"response\"", "\"status\":\"completed\"", "\"output_text\":\"answer\"",
        "\"function_call\"", "\"type\":\"summary_text\"", "\"cached_tokens\":2",
    };
    static const char *const responses_stream_markers[] = {
        "response.completed", "\"status\":\"completed\"", "\"output_text\":\"answer\"", "\"output_tokens\":3",
    };
    static const char *const anthropic_non_markers[] = {
        "\"type\":\"message\"", "\"type\":\"thinking\"", "\"type\":\"tool_use\"", "\"stop_reason\":\"tool_use\"",
        "\"input_tokens\":7", "\"cache_read_input_tokens\":2", "\"cache_creation_input_tokens\":1",
    };
    static const char *const anthropic_stream_markers[] = {
        "thinking_delta", "input_json_delta", "\"stop_reason\":\"end_turn\"", "message_stop",
        "\"cache_read_input_tokens\":2", "\"cache_creation_input_tokens\":1",
    };

    if (argc != 2) {
        return fail("usage: load-plugin-smoke <plugin.so>");
    }

    if (setenv("CC_API_BASE", "https://api.commandcode.ai", 1) != 0 ||
        setenv("CC_FINGERPRINT_SALT", "", 1) != 0) {
        return fail("could not establish deterministic Command Code smoke environment");
    }

    smoke_host_init(&host);
    if (run_wire_rejection_fixtures(&host)) {
        smoke_host_destroy(&host);
        return 1;
    }
    host_api.abi_version = 1;
    host_api.host_ctx = &host;
    host_api.call = smoke_host_call;
    host_api.free_buffer = smoke_host_free;

    handle = dlopen(argv[1], RTLD_NOW | RTLD_LOCAL);
    if (handle == NULL) {
        fprintf(stderr, "dlopen failed: %s\n", dlerror());
        smoke_host_destroy(&host);
        return 1;
    }

    dlerror();
    init = (cliproxy_plugin_init_fn)dlsym(handle, "cliproxy_plugin_init");
    symbol_error = dlerror();
    if (symbol_error != NULL || init == NULL) {
        fprintf(stderr, "cliproxy_plugin_init was not found: %s\n", symbol_error != NULL ? symbol_error : "unknown error");
        dlclose(handle);
        smoke_host_destroy(&host);
        return 1;
    }

    if (init(&host_api, &plugin) != 0) {
        dlclose(handle);
        smoke_host_destroy(&host);
        return fail("cliproxy_plugin_init failed");
    }
    if (plugin.abi_version != 1 || plugin.call == NULL || plugin.free_buffer == NULL || plugin.shutdown == NULL) {
        if (plugin.shutdown != NULL) {
            plugin.shutdown();
        }
        dlclose(handle);
        smoke_host_destroy(&host);
        return fail("plugin ABI table is incomplete or incompatible");
    }

    snprintf(register_request, sizeof(register_request), "{\"config_yaml\":\"%s\"}", config_base64);
    if (invoke_plugin(&plugin, register_method, register_request, &response) != 0 ||
        response.ptr == NULL || !bytes_contains(response.ptr, response.len, "\"ok\":true")) {
        if (response.ptr != NULL) {
            plugin.free_buffer(response.ptr, response.len);
        }
        plugin.shutdown();
        dlclose(handle);
        smoke_host_destroy(&host);
        return fail("plugin registration RPC failed");
    }
    plugin.free_buffer(response.ptr, response.len);
    response.ptr = NULL;
    response.len = 0;

    if (invoke_plugin(&plugin, identifier_method, NULL, &response) != 0 || response.ptr == NULL || response.len == 0 ||
        !bytes_contains(response.ptr, response.len, "command-code")) {
        if (response.ptr != NULL) {
            plugin.free_buffer(response.ptr, response.len);
        }
        plugin.shutdown();
        dlclose(handle);
        smoke_host_destroy(&host);
        return fail("plugin identifier RPC failed");
    }
    plugin.free_buffer(response.ptr, response.len);

    failed |= run_required_path(&plugin, &host, "chat/non-stream", "executor.execute", "smoke-model", "chat-completions", 0,
                                chat_non_payload_base64,
                                NULL, chat_non_markers, sizeof(chat_non_markers) / sizeof(chat_non_markers[0]));
    failed |= run_required_path(&plugin, &host, "chat/stream", "executor.execute_stream", "smoke-model", "chat-completions", 1,
                                chat_stream_payload_base64,
                                "rpc-chat-stream", chat_stream_markers, sizeof(chat_stream_markers) / sizeof(chat_stream_markers[0]));
    failed |= run_required_path(&plugin, &host, "responses/non-stream", "executor.execute", "gpt-smoke", "responses", 0,
                                responses_non_payload_base64,
                                NULL, responses_non_markers, sizeof(responses_non_markers) / sizeof(responses_non_markers[0]));
    failed |= run_required_path(&plugin, &host, "responses/stream", "executor.execute_stream", "gpt-smoke", "responses", 1,
                                responses_stream_payload_base64,
                                "rpc-responses-stream", responses_stream_markers, sizeof(responses_stream_markers) / sizeof(responses_stream_markers[0]));
    failed |= run_required_path(&plugin, &host, "anthropic/non-stream", "executor.execute", "claude-smoke", "anthropic", 0,
                                anthropic_non_payload_base64,
                                NULL, anthropic_non_markers, sizeof(anthropic_non_markers) / sizeof(anthropic_non_markers[0]));
    failed |= run_required_path(&plugin, &host, "anthropic/stream", "executor.execute_stream", "claude-smoke", "anthropic", 1,
                                anthropic_stream_payload_base64,
                                "rpc-anthropic-stream", anthropic_stream_markers, sizeof(anthropic_stream_markers) / sizeof(anthropic_stream_markers[0]));

    failed |= require_rpc_paths();
    failed |= run_error_fixture(&plugin);
    failed |= run_cancellation_fixture(&plugin, &host);
    failed |= validate_host(&host);

    plugin.shutdown();
    if (dlclose(handle) != 0) {
        failed |= fail("dlclose failed");
    }
    smoke_host_destroy(&host);
    return failed ? 1 : 0;
}
