package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const managementStatusPath = "/v0/management/plugins/" + pluginName + resourcePath
const publicStatusPath = "/v0/resource/plugins/" + pluginName + resourcePath

func privateHeaders(contentType string) http.Header {
	return http.Header{
		"Content-Type":           {contentType},
		"Cache-Control":          {"no-store"},
		"Referrer-Policy":        {"no-referrer"},
		"X-Content-Type-Options": {"nosniff"},
	}
}

func managementJSON(status int, value any) ([]byte, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return okEnvelope(managementResponse{StatusCode: status, Headers: privateHeaders("application/json; charset=utf-8"), Body: body})
}

func accountAlias(id string) string {
	sum := sha256.Sum256([]byte(id))
	return fmt.Sprintf("account-%x", sum[:8])
}

func resolveStatusAuthID(id string) string {
	files, err := currentRuntime().host.AuthList()
	if err == nil {
		for _, file := range files {
			if isCodexAuth(file) && accountAlias(file.ID) == id {
				return file.ID
			}
		}
	}
	return id
}

// Opaque aliases keep account selection usable even when an auth ID or filename
// itself embeds an email address. All tables use the same alias.
func redactStatusIdentities(view *statusView, cfg pluginConfig) {
	if cfg.ShowAccountDetails {
		return
	}
	for i := range view.AuthIDs {
		view.AuthIDs[i] = accountAlias(view.AuthIDs[i])
	}
	for i := range view.States {
		view.States[i].AuthID = accountAlias(view.States[i].AuthID)
	}
	for i := range view.ExpiredStates {
		view.ExpiredStates[i].AuthID = accountAlias(view.ExpiredStates[i].AuthID)
	}
	for i := range view.Probes {
		view.Probes[i].AuthID = accountAlias(view.Probes[i].AuthID)
	}
	for i := range view.ProbeLogs {
		view.ProbeLogs[i].AuthID = accountAlias(view.ProbeLogs[i].AuthID)
	}
	for i := range view.Injections {
		view.Injections[i].AuthID = accountAlias(view.Injections[i].AuthID)
	}
	for i := range view.Accounts {
		view.Accounts[i].AuthID = accountAlias(view.Accounts[i].AuthID)
		view.Accounts[i].Label = view.Accounts[i].AuthID
	}
	for i := range view.Auths {
		auth := &view.Auths[i]
		auth.ID = accountAlias(auth.ID)
		auth.AuthIndex = ""
		auth.Name = auth.ID
		auth.Label = auth.ID
		auth.Email = ""
	}
}

func visibleInjectionHeaders(raw string, cfg pluginConfig) string {
	if !cfg.ShowInjectionHeaders {
		return ""
	}
	var headers http.Header
	if json.Unmarshal([]byte(raw), &headers) != nil {
		return ""
	}
	for key := range headers {
		switch strings.ToLower(key) {
		case "authorization", "proxy-authorization", "cookie", "set-cookie", "x-api-key", "x-management-key":
			headers[key] = []string{"[redacted]"}
		case "x-codex-turn-state":
			if !cfg.showStateValuesEnabled() {
				headers[key] = []string{"[redacted]"}
			}
		}
	}
	out, err := json.Marshal(headers)
	if err != nil {
		return ""
	}
	return string(out)
}

// Upstream and transport errors can echo credentials, states or account names.
// Keep detailed errors in local logs instead of copying arbitrary text to HTTP.
func visibleStatusError(raw string) string {
	if raw == "" {
		return ""
	}
	switch classifyFailure(0, raw) {
	case failureQuota:
		return "Account quota exhausted; probing is subject to quota backoff."
	case failureTransient:
		return "Upstream temporarily overloaded."
	default:
		return "Operation failed; inspect local plugin logs for details."
	}
}

// Public resource: static content only. The management key lives in this page's
// closure until logout/reload and is sent solely as an Authorization header.
const statusLoginPage = `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Codex Turn State</title>
<style>body{margin:0;font:14px system-ui;background:#f6f7f9;color:#111827}header{padding:16px;background:white}input,button{padding:8px;margin:4px}iframe{width:100%;height:85vh;border:0}#message{margin:8px}</style></head><body>
<header><strong>Codex Turn State</strong><form id="login"><label>CPA 管理密钥 <input id="key" type="password" autocomplete="off" required></label><button>登录</button></form><button id="logout" hidden>退出</button><p id="message">请输入 CPA 管理密钥。密钥仅保留在本页内存；刷新页面后需重新登录。</p></header><iframe id="status" title="状态页" hidden></iframe>
<script>(function(){'use strict';
var key='',generation=0,frame=document.getElementById('status'),form=document.getElementById('login'),input=document.getElementById('key'),message=document.getElementById('message'),logout=document.getElementById('logout');
var endpoint=location.pathname.replace('/v0/resource/','/v0/management/');
function clear(){generation++;key='';input.value='';frame.hidden=true;frame.removeAttribute('srcdoc');form.hidden=false;logout.hidden=true;}
window.ctsFetch=function(path,options){
 var opts=Object.assign({},options||{}),query=path.indexOf('?'),url=endpoint+(query<0?'':path.slice(query));
 opts.headers=new Headers(opts.headers||{});opts.headers.set('Authorization','Bearer '+key);opts.cache='no-store';opts.credentials='omit';opts.redirect='error';
 return window.fetch(url,opts).then(function(response){if(!response.ok){if(response.status===401||response.status===403){clear();message.textContent='管理密钥无效或远程管理未启用。';}throw new Error('HTTP '+response.status);}return response;});
};
window.ctsReload=function(){var current=generation;return window.ctsFetch(endpoint).then(function(r){return r.text();}).then(function(page){if(current!==generation)return;frame.srcdoc=page;frame.hidden=false;form.hidden=true;logout.hidden=false;message.textContent='已登录；数据由 CPA 管理接口保护。';});};
form.addEventListener('submit',function(e){e.preventDefault();key=input.value.trim();input.value='';generation++;window.ctsReload().catch(function(){message.textContent='登录失败。请检查管理密钥、管理接口和远程访问设置。';});});
logout.addEventListener('click',function(){clear();message.textContent='已退出。';});
function hostedKey(raw){if(!raw)return '';var v=raw;try{v=JSON.parse(raw);}catch(e){}if(typeof v!=='string'){v=v&&(v.managementKey||v.management_key||v.apiKey||v.api_key||v.token||v.key||v.Authorization)||'';}v=String(v||'').trim();if(/^Bearer\s+/i.test(v))v=v.replace(/^Bearer\s+/i,'');return v;}
function tryHostedLogin(){var found=hostedKey(window.localStorage&&localStorage.getItem('cli-proxy-auth'));if(!found){return false;}key=found;generation++;window.ctsReload().then(function(){input.value='';}).catch(function(){key='';form.hidden=false;logout.hidden=true;message.textContent='自动登录失败，请手动输入 CPA 管理密钥。';});return true;}
if(!tryHostedLogin()){form.hidden=false;}
})();</script></body></html>`
