// 设计图脚本：用真·大肥鱼皮把右侧形象台活起来。
// 皮与渲染库都在 data/host/ 下（不入库，授权见 README），故本页面必须经 http 打开。
//
// 加载写法照搬主仓已验证的叠层（controller/host.go:2162 boot）：
//  1. 配置键是 models[]，舞台尺寸固定写成竖屏 1080×1920——库会按 16:9 反推宽度，给小尺寸就压扁。
//  2. 库把 #oml2d-stage 挂到 body 下，必须周期搬进侧栏，否则它会铺满整页。
//  3. coreModel 是**函数**不是属性（主仓 host.go:2238 用的是 im.coreModel）：
//     当属性比对会永远不相等，钩子一次都挂不上——水印就是这么漏掉的。
(function () {
  var MODEL = "../../data/host/model/大肥鱼/大肥鱼.model3.json";
  // 作者把水印烤进了贴图，内置"水印消除"表情=把 Paramcol_3 置 1（已核 EXP/13 水印消除.exp3.json）。
  var HOLD = { Paramcol_3: 1 };

  function hush() {
    document.querySelectorAll('[class*="oml2d"]').forEach(function (el) {
      var n = (el.className || "") + " " + (el.id || "");
      if (/tips|status|menu|loading|icon/.test(n)) el.style.setProperty("display", "none", "important");
    });
  }

  function adopt() {
    var st = document.getElementById("oml2d-stage");
    var box = document.getElementById("live2d");
    if (st && box && st.parentElement !== box) box.appendChild(st);
    hush();
  }

  function fallback(msg) {
    document.getElementById("live2d").innerHTML =
      '<div class="nofallback">形象没起来：' + (msg || "未知原因") + '</div>';
  }

  if (!window.OML2D || !window.OML2D.loadOml2d) { fallback("oml.min.js 没加载"); return; }

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
    fallback(String(e && e.message || e)); return;
  }

  var core = null, hooked = null, phase = 0, held = false;

  function apply() {
    if (!core) return;
    for (var k in HOLD) {
      try {
        core.setParameterValueById(k, HOLD[k]);
        // 写进去不等于按住：读回来确认，否则角标会谎报"水印已消除"。
        if (typeof core.getParameterValueById === "function" && core.getParameterValueById(k) >= 0.99) held = true;
      } catch (e) {}
    }
    // 设计图里"正在说话"是假的：固定正弦当包络，只为让嘴动起来。
    phase += 0.22;
    var val = 0.5 * (0.5 + 0.5 * Math.sin(phase));
    try { core.setParameterValueById("ParamMouthOpenY", val); } catch (e) {}
  }

  // 库在 resize 时会换模型实例，钩子只挂一次就会挂在旧的上面（表现：一开始正常，后来不动）。
  function poll() {
    var oml = window.__oml;
    var mod = oml && oml.models && oml.models.model;
    var im = mod && mod.internalModel;
    var c = im && typeof im.coreModel === "function" ? im.coreModel() : (im && im.coreModel);
    if (im && c && im !== hooked) {
      core = c; hooked = im;
      try { im.on("beforeModelUpdate", apply); } catch (e) {}
      // 参数写不动时退回表情：表情本身就是把同一个参数置 1。
      if (!held) { try { mod.setExpression && mod.setExpression("13 水印消除"); } catch (e) {} }
    }
    adopt();
    setTimeout(poll, 800);
  }
  poll();

  setTimeout(function () {
    if (!document.querySelector("#live2d canvas")) fallback("模型没渲染出来");
    // 水印到底关掉没有，得能在画面上自证，不能只说"我写了参数"。
    var b = document.querySelector(".credit");
    if (b) b.textContent = held ? "模型：Mao绒绒家园" : "模型：Mao绒绒家园 · 水印未消除";
  }, 6000);
})();
