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

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

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
	"encoding/json"
	"fmt"
	"net/http"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type lifecycleRequest struct {
	ConfigYAML    []byte `json:"config_yaml"`
	SchemaVersion uint32 `json:"schema_version"`
}

type registration struct {
	SchemaVersion uint32                   `json:"schema_version"`
	Metadata      pluginapi.Metadata       `json:"metadata"`
	Capabilities  registrationCapabilities `json:"capabilities"`
}

type registrationCapabilities struct {
	RequestInterceptor        bool `json:"request_interceptor"`
	ResponseInterceptor       bool `json:"response_interceptor"`
	StreamChunkInterceptor    bool `json:"response_stream_interceptor"`
	WebSocketResponseObserver bool `json:"websocket_response_observer"`
	ManagementAPI             bool `json:"management_api"`
	UsagePlugin               bool `json:"usage_plugin"`
}

type liveHost struct{}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	C.store_host_api(host)
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
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
	raw, errHandle := handleMethod(C.GoString(method), requestBytes)
	if errHandle != nil {
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, len C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
	_ = len
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	currentRuntime().shutdown()
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if errConfigure := configure(request); errConfigure != nil {
			return nil, errConfigure
		}
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodRequestInterceptBefore:
		return interceptBeforeAuth(request)
	case pluginabi.MethodRequestInterceptAfter:
		return interceptAfterAuth(request)
	case pluginabi.MethodResponseInterceptAfter:
		return interceptResponse(request)
	case pluginabi.MethodResponseInterceptStreamChunk:
		return interceptStreamChunk(request)
	case pluginabi.MethodWebSocketResponseEvent:
		return observeWebSocket(request)
	case pluginabi.MethodUsageHandle:
		return handleUsage(request)
	case pluginabi.MethodManagementRegister:
		return okEnvelope(managementRegistration{
			Routes: []managementRoute{
				{Method: http.MethodGet, Path: managementStatusPath},
				{Method: http.MethodPost, Path: managementStatusPath},
			},
			Resources: []managementResource{{
				Path:        resourcePath,
				Menu:        "Codex Turn State",
				Description: "Codex Turn State 登录页；数据与操作由 CPA 管理密钥保护。",
			}},
		})
	case pluginabi.MethodManagementHandle:
		return handleManagement(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func configure(raw []byte) error {
	var req lifecycleRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return errUnmarshal
		}
	}
	if req.SchemaVersion > 0 && req.SchemaVersion < pluginabi.SchemaVersionWebSocketResponseObserver {
		return fmt.Errorf("codex-turn-state requires host schema version %d or newer", pluginabi.SchemaVersionWebSocketResponseObserver)
	}
	cfg := pluginConfig{}
	if len(req.ConfigYAML) > 0 {
		if errUnmarshal := yaml.Unmarshal(req.ConfigYAML, &cfg); errUnmarshal != nil {
			return errUnmarshal
		}
	}
	return currentRuntime().applyConfig(cfg)
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             pluginName,
			Version:          pluginVersion,
			Author:           "spacex-3",
			GitHubRepository: "https://github.com/spacex-3/cts-plugin",
			Logo:             "https://raw.githubusercontent.com/router-for-me/CLIProxyAPI/main/docs/logo.png",
			ConfigFields: []pluginapi.ConfigField{
				{Name: "proxy", Type: pluginapi.ConfigFieldTypeString, Description: "兼容配置：多个代理每行一个。建议改用下方的 proxies 列表，避免单行输入框把多行显示成一行。"},
				{Name: "proxies", Type: pluginapi.ConfigFieldTypeArray, Description: "代理 JSON 数组，例如 [\"host:port:user:pw\", \"host2:port:user:pw\"]。每项格式为 host:port:user:password 或 http/https/socks5 URL。未命中时自动轮询下一条。"},
				{Name: "proxy_scheme", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{"http", "https", "socks5", "socks5h"}, Description: "proxy/proxies 条目未写协议前缀时使用的默认协议。默认 http；BestGo 等 SOCKS5 节点请选 socks5。"},
				{Name: "auth_ids", Type: pluginapi.ConfigFieldTypeArray, Description: "需要探测、采集和注入的 Codex 运行时 auth ID。留空表示选择全部 Codex 账号。"},
				{Name: "probe_auth_ids", Type: pluginapi.ConfigFieldTypeArray, Description: "仅参与探测的 Codex auth ID。留空表示所有在 auth_ids 范围内的账号都探测；也可直接在状态页勾选保存。"},
				{Name: "models", Type: pluginapi.ConfigFieldTypeArray, Description: "需要探测和注入的模型。默认 gpt-5.6-sol 和 gpt-6-astra。"},
				{Name: "interval_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "自动探测间隔（秒）。默认 1800，即 30 分钟。"},
				{Name: "target_state_length", Type: pluginapi.ConfigFieldTypeInteger, Description: "接受的 x-codex-turn-state 长度。默认 292。"},
				{Name: "accepted_blocks", Type: pluginapi.ConfigFieldTypeArray, Description: "按 Fernet 密文块数接受的 state 类型，默认 [10,12]：10 为 Pro/Plus（292），12 为 Team（332）。空数组回退默认值。"},
				{Name: "ttl_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "已缓存 state 的有效期（秒）。默认 3600，即约 1 小时。"},
				{Name: "inject", Type: pluginapi.ConfigFieldTypeBoolean, Description: "把缓存的 turn state 注入后续 Codex 生产请求，用于保持不降智。默认开启。"},
				{Name: "harvest", Type: pluginapi.ConfigFieldTypeBoolean, Description: "从正常 CPA Codex 流量中采集命中目标长度的 state。默认开启。"},
				{Name: "probe", Type: pluginapi.ConfigFieldTypeBoolean, Description: "运行代理探测循环，主动获取 state。默认开启。"},
				{Name: "direct_probe", Type: pluginapi.ConfigFieldTypeBoolean, Description: "每次代理探测前先记录一次无代理基线，用于对照，不会缓存。默认关闭。"},
				{Name: "show_account_details", Type: pluginapi.ConfigFieldTypeBoolean, Description: "在受保护的状态页显示真实账号 ID、名称和邮箱。默认使用不透明别名。"},
				{Name: "show_injection_headers", Type: pluginapi.ConfigFieldTypeBoolean, Description: "在受保护的状态页显示注入请求头。默认关闭；state 仍受 show_state_values 控制，凭据头始终隐藏。"},
				{Name: "show_state_values", Type: pluginapi.ConfigFieldTypeBoolean, Description: "在状态页和 JSON 中显示完整 state 值。涉及敏感信息，仅在可信环境开启。默认关闭。"},
				{Name: "probe_schedule", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{"fixed", "state_aware", "on_demand"}, Description: "探测调度方式。fixed 为固定间隔；state_aware 在已持有新鲜 state 时跳过周期探测，等接近过期再续期。on_demand 无定时或启动探测，由请求触发。默认 fixed。"},
				{Name: "probe_wait_milliseconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "on_demand 请求等待上限（毫秒）。默认 1500，负数只排队不等待。"},
				{Name: "probe_timeout_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "on_demand 后台探测总超时（秒）。默认 60。"},
				{Name: "on_demand_cooldown_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "on_demand 下，某账号+模型连续 3 轮未命中后的探测冷却（秒）。默认 600。"},
				{Name: "use_issued_at", Type: pluginapi.ConfigFieldTypeBoolean, Description: "按 Fernet 签发时间判断新鲜度；拒绝无效或过期 state。默认关闭。"},
				{Name: "require_completed", Type: pluginapi.ConfigFieldTypeBoolean, Description: "仅在 response.completed / completed 响应后采集 state。默认关闭。"},
				{Name: "error_aware_backoff", Type: pluginapi.ConfigFieldTypeBoolean, Description: "临时错误提前重探，额度不足按账号退避。默认关闭。"},
				{Name: "quota_backoff_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "额度不足后的探测退避（秒）。默认 900。刷新 state 无法恢复额度。"},
				{Name: "rotate_proxy_start", Type: pluginapi.ConfigFieldTypeBoolean, Description: "跨轮递增代理池起点。默认关闭。"},
				{Name: "probe_lead_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "state_aware / on_demand 模式下，在 state 过期前提前多少秒开始探测。默认 300。"},
				{Name: "probe_log_limit", Type: pluginapi.ConfigFieldTypeInteger, Description: "内存中保留的探测日志条数。默认 200，最大 1000。"},
				{Name: "max_probe_attempts", Type: pluginapi.ConfigFieldTypeInteger, Description: "每个账号+模型在一轮探测中的尝试次数。默认 3。"},
				{Name: "failure_reprobe_threshold", Type: pluginapi.ConfigFieldTypeInteger, Description: "倒计时窗口内连续失败多少次后自动重新探测。默认 3；设为负数可关闭。"},
				{Name: "max_output_tokens", Type: pluginapi.ConfigFieldTypeInteger, Description: "已废弃并忽略；Codex 上游拒绝 token limit 字段。"},
				{Name: "prompt", Type: pluginapi.ConfigFieldTypeString, Description: "每次探测发送的最小提示词。默认是一个句号。"},
			},
		},
		Capabilities: registrationCapabilities{
			RequestInterceptor:        true,
			ResponseInterceptor:       true,
			StreamChunkInterceptor:    true,
			WebSocketResponseObserver: true,
			ManagementAPI:             true,
			UsagePlugin:               true,
		},
	}
}

func (liveHost) AuthList() ([]pluginapi.HostAuthFileEntry, error) {
	result, errCall := callHost(pluginabi.MethodHostAuthList, map[string]any{})
	if errCall != nil {
		return nil, errCall
	}
	var resp authListResponse
	if errUnmarshal := json.Unmarshal(result, &resp); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host.auth.list result: %w", errUnmarshal)
	}
	return resp.Files, nil
}

func (liveHost) AuthGet(authIndex string) (pluginapi.HostAuthGetResponse, error) {
	result, errCall := callHost(pluginabi.MethodHostAuthGet, pluginapi.HostAuthGetRequest{AuthIndex: authIndex})
	if errCall != nil {
		return pluginapi.HostAuthGetResponse{}, errCall
	}
	var resp pluginapi.HostAuthGetResponse
	if errUnmarshal := json.Unmarshal(result, &resp); errUnmarshal != nil {
		return pluginapi.HostAuthGetResponse{}, fmt.Errorf("decode host.auth.get result: %w", errUnmarshal)
	}
	return resp, nil
}

func (liveHost) Log(level, message string, fields map[string]any) {
	_, _ = callHost(pluginabi.MethodHostLog, map[string]any{
		"level":   level,
		"message": message,
		"fields":  fields,
	})
}

func callHost(method string, payload any) (json.RawMessage, error) {
	rawPayload, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return nil, fmt.Errorf("marshal host callback payload %s: %w", method, errMarshal)
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))

	var response C.cliproxy_buffer
	var requestPtr *C.uint8_t
	if len(rawPayload) > 0 {
		cPayload := C.CBytes(rawPayload)
		if cPayload == nil {
			return nil, fmt.Errorf("allocate host callback payload %s", method)
		}
		defer C.free(cPayload)
		requestPtr = (*C.uint8_t)(cPayload)
	}
	callCode := C.call_host_api(cMethod, requestPtr, C.size_t(len(rawPayload)), &response)
	var rawResponse []byte
	if response.ptr != nil && response.len > 0 {
		rawResponse = C.GoBytes(response.ptr, C.int(response.len))
	}
	if response.ptr != nil {
		C.free_host_buffer(response.ptr, response.len)
	}
	if len(rawResponse) == 0 {
		return nil, fmt.Errorf("host callback %s returned no response, code=%d", method, int(callCode))
	}

	var env envelope
	if errUnmarshal := json.Unmarshal(rawResponse, &env); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host callback envelope %s: %w", method, errUnmarshal)
	}
	if !env.OK {
		if env.Error != nil {
			return nil, fmt.Errorf("%s: %s", env.Error.Code, env.Error.Message)
		}
		return nil, fmt.Errorf("host callback %s failed", method)
	}
	if callCode != 0 {
		return nil, fmt.Errorf("host callback %s returned code=%d", method, int(callCode))
	}
	return append(json.RawMessage(nil), env.Result...), nil
}

func okEnvelope(v any) ([]byte, error) {
	raw, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
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

func cloneHeader(headers http.Header) http.Header {
	if headers == nil {
		return nil
	}
	cloned := make(http.Header, len(headers))
	for key, values := range headers {
		cloned[key] = append([]string(nil), values...)
	}
	return cloned
}
