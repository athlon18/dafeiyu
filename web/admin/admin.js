// 设计图脚本：切页 + 用真·大肥鱼皮把右侧形象台活起来。
// 皮与渲染库都在 data/host/ 下（不入库，授权见 README），故本页面必须经 http 打开。
//
// 加载写法照搬主仓已验证的叠层（controller/host.go:2162 boot）：
// 配置键是 models[]，舞台尺寸必须**固定写成竖屏 1080×1920**——库会按 16:9 反推宽度，
// 给小尺寸就压扁、跑偏；缩放交给外层 transform，别拿库的 scale 当布局用。
(function () {
  var MODEL = "../../data/host/model/大肥鱼/大肥鱼.model3.json";
  var TEXTURE = "../../data/host/model/大肥鱼/大肥鱼.8192/texture_00.png";
  // 作者把水印烤进了贴图，内置"水印消除"表情=把这个参数置 1；每帧写，idle 盖不回来。
  var HOLD = { Paramcol_3: 1 };

  function show(name) {
    var btn = document.querySelector('.tab[data-tab="' + name + '"]');
    var panel = document.querySelector('[data-panel="' + name + '"]');
    if (!btn || !panel) return;
    document.querySelectorAll(".tab").forEach(function (b) { b.classList.remove("active"); });
    document.querySelectorAll(".panel").forEach(function (p) { p.classList.remove("active"); });
    btn.classList.add("active");
    panel.classList.add("active");
  }

  // 出图用：?tab=rules 直接落在那一页，省得每张图都要模拟点击。
  var want = new URLSearchParams(location.search).get("tab");
  if (want) show(want);

  // 库只认 body：舞台挂在文档根上，得搬进侧栏里才能被裁住。resize 后会重挂，所以周期检查。
  function adopt() {
    var st = document.getElementById("oml2d-stage");
    var box = document.getElementById("live2d");
    if (st && box && st.parentElement !== box) box.appendChild(st);
    // 看板娘的菜单/气泡/状态条在后台预览里是噪声；类名实测不止一套，CSS 选择器会漏，这里兜一层。
    document.querySelectorAll('[class*="oml2d"]').forEach(function (el) {
      var n = (el.className || "") + " " + (el.id || "");
      if (/tips|status|menu|loading|icon/.test(n)) el.style.setProperty("display", "none", "important");
    });
  }

  function hush() {
    document.querySelectorAll('[class*="oml2d"]').forEach(function (el) {
      var n = (el.className || "") + " " + (el.id || "");
      if (/tips|status|menu|loading|icon/.test(n)) el.style.setProperty("display", "none", "important");
    });
  }

  function fallback() {
    document.getElementById("live2d").innerHTML =
      '<div class="nofallback">形象没起来：' + (window.__err || "未知原因") + '</div>';
  }

  if (!window.OML2D || !window.OML2D.loadOml2d) {
    window.__err = "oml.min.js 没加载";
    fallback(); return;
  }

  try {
    window.__oml = window.OML2D.loadOml2d({
      models: [{
        path: MODEL,
        scale: 0.4,
        position: [0, 0],
        stageStyle: { width: "1080px", height: "1920px" }
      }],
      menus: { disable: true },
      tips: { disable: true },
      statusBar: { disable: true }
    });
  } catch (e) {
    window.__err = String(e && e.message || e);
    fallback(); return;
  }

  // 口型 + 默认参数：每帧 update 前写，表情/idle 动作盖不回来。
  var core = null, val = 0, phase = 0;
  function frame() {
    var oml = window.__oml;
    var mod = oml && oml.models && oml.models.model;
    var im = mod && mod.internalModel;
    if (im && im.coreModel && im.coreModel !== core) {
      core = im.coreModel;
      try { im.on("beforeModelUpdate", apply); } catch (e) {}
    }
    adopt();
    setTimeout(frame, 800); // 库在 resize 时会换实例，钩子得跟着重新挂
  }
  function apply() {
    if (!core) return;
    for (var k in HOLD) {
      try { core.setParameterValueById(k, HOLD[k]); } catch (e) {}
    }
    // 设计图里"正在说话"是假的：用固定正弦当包络，只为让嘴动起来。
    phase += 0.22;
    val = 0.5 * (0.5 + 0.5 * Math.sin(phase));
    try { core.setParameterValueById("ParamMouthOpenY", val); } catch (e) {}
    try { core.setParameterValueById("PARAM_MOUTH_OPEN_Y", val); } catch (e) {}
  }
  frame();

  setInterval(adopt, 1000);
  setInterval(hush, 500);
  setTimeout(function () {
    if (!document.querySelector("#live2d canvas")) fallback();
  }, 8000);
})();
