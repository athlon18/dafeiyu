package tts

import "testing"

// 音色清单的回法有好几种（本地网关给 voices、有的网关给 data、还有直接回数组的），
// 解析要都认；认不出时必须报错而不是回空列表——空列表在页面上等于「这个服务没音色」，
// 会把「地址填错」误诊成「服务是空的」。
func TestParseVoicesShapes(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{"voices 包裹", `{"voices":[{"id":"a","name":"甲","note":"商用","commercial":true}]}`, 1},
		{"data 包裹", `{"data":[{"id":"a"},{"id":"b","name":"乙"}]}`, 2},
		{"裸数组", `[{"id":"a"},{"id":"b"},{"id":"c"}]`, 3},
	}
	for _, c := range cases {
		got, err := parseVoices([]byte(c.body))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(got) != c.want {
			t.Fatalf("%s: 得到 %d 条，想要 %d 条", c.name, len(got), c.want)
		}
	}
}

func TestParseVoicesDefaults(t *testing.T) {
	got, err := parseVoices([]byte(`{"voices":[{"id":"diadia_xuemei","commercial":true},{"name":"没 id"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("没 id 的项应被丢掉，实际留下 %d 条", len(got))
	}
	if got[0].Name != "diadia_xuemei" {
		t.Fatalf("缺 name 时应回落到 id，实际 %q", got[0].Name)
	}
	if !got[0].Commercial {
		t.Fatal("commercial 应原样透传（页面靠它标商用）")
	}
}

func TestParseVoicesRejectsGarbage(t *testing.T) {
	if _, err := parseVoices([]byte(`<html>404</html>`)); err == nil {
		t.Fatal("HTML 错误页必须报错，不能当成空清单")
	}
	if _, err := parseVoices([]byte(`{"voices":[]}`)); err == nil {
		t.Fatal("空清单必须报错，否则页面分不清「地址错了」和「服务真是空的」")
	}
}

func TestVoicesURL(t *testing.T) {
	cases := map[string]string{
		"http://127.0.0.1:9880/v1":        "http://127.0.0.1:9880/v1/voices",
		"http://127.0.0.1:9880/v1/":       "http://127.0.0.1:9880/v1/voices",
		"http://127.0.0.1:9881/v1/voices": "http://127.0.0.1:9881/v1/voices",
		"https://x/v1/audio/speech":       "https://x/v1/voices",
	}
	for in, want := range cases {
		if got := voicesURL(in); got != want {
			t.Fatalf("voicesURL(%q)=%q，想要 %q", in, got, want)
		}
	}
}
