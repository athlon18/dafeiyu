package skin

import "testing"

// 大肥鱼这张皮有两个必须守住的口径：署名文案、水印参数。
// 它们直接决定 OBS 画面上有没有那行小字、以及角色身上会不会带作者水印。
func TestDafeiyuCarriesCreditAndWatermarkParam(t *testing.T) {
	if got := Credit("大肥鱼"); got != "模型：Mao绒绒家园" {
		t.Fatalf("署名口径变了: %q", got)
	}
	p := Params("大肥鱼")
	if v, ok := p["Paramcol_3"]; !ok || v != 1 {
		t.Fatalf("水印消除参数不对: %v", p)
	}
}

// 名单外 = 来源未知：不许凭空给一句"看起来没问题"的授权文案。
func TestUnknownSkinHasNoLicenseText(t *testing.T) {
	if got := Credit("某拆包皮"); got != "" {
		t.Fatalf("未知形象不该有署名: %q", got)
	}
	if len(Params("某拆包皮")) != 0 {
		t.Fatal("未知形象不该有默认参数")
	}
	if _, ok := Licenses["某拆包皮"]; ok {
		t.Fatal("名单被查到了东西")
	}
}
