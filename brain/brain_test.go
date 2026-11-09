package brain

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNoKeyMeansNoLLM(t *testing.T) {
	if th := New(Config{}); th != nil {
		t.Fatal("没配 key 却返回了 Thinker，上层会以为能生成")
	}
}

func TestThinkReturnsContent(t *testing.T) {
	var gotAuth, gotPath, gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotModel, _ = body["model"].(string)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": " 在的，马上开一轮 "}}},
		})
	}))
	defer srv.Close()

	th := New(Config{APIKey: "k", BaseURL: srv.URL, Model: "m", TimeoutSec: 5})
	if th == nil {
		t.Fatal("配了 key 却没构造出来")
	}
	out, err := th.Think(context.Background(), "你是大肥鱼", "观众问：今天玩什么")
	if err != nil {
		t.Fatal(err)
	}
	if out != "在的，马上开一轮" {
		t.Fatalf("没 trim: %q", out)
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("路径不对: %s", gotPath)
	}
	if gotAuth != "Bearer k" {
		t.Fatalf("没带 key: %q", gotAuth)
	}
	if gotModel != "m" {
		t.Fatalf("模型没传: %q", gotModel)
	}
}

func TestEmptyContentIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "   "}}},
		})
	}))
	defer srv.Close()
	if _, err := New(Config{APIKey: "k", BaseURL: srv.URL, TimeoutSec: 5}).Think(context.Background(), "s", "u"); err == nil {
		t.Fatal("空回复必须报错，让上层别说空气")
	}
}

func TestSystemPromptFallbackToPersona(t *testing.T) {
	var gotSys string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, m := range body.Messages {
			if m.Role == "system" {
				gotSys = m.Content
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "好"}}},
		})
	}))
	defer srv.Close()

	th := New(Config{APIKey: "k", BaseURL: srv.URL, Persona: "你是直播间里的大肥鱼"})
	if _, err := th.Think(context.Background(), "", "问"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotSys, "大肥鱼") {
		t.Fatalf("编排器没给 system 时应回落到人设，实际 %q", gotSys)
	}
}

func TestHTTPErrorSurfaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream down", http.StatusBadGateway)
	}))
	defer srv.Close()
	_, err := New(Config{APIKey: "k", BaseURL: srv.URL, TimeoutSec: 5}).Think(context.Background(), "s", "u")
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("网关错误要能看出来，实际 %v", err)
	}
}
