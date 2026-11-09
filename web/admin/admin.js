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

  // 试听一句：真打接口，点了必须有反馈（成功带毫秒数，失败显示原因）。
  // 服务没起时就明写"服务没起"，绝不留一个按下去没反应的哑按钮。
  var API = "http://127.0.0.1:8420";
  var btn = document.querySelector(".pane button.wide");
  var voiceSel = document.querySelectorAll(".pane select")[0];
  var devSel = document.querySelectorAll(".pane select")[1];
  if (btn) {
    btn.addEventListener("click", function () {
      var label = btn.textContent;
      btn.disabled = true;
      btn.textContent = "合成中…";
      var url = API + "/tts/preview?text=" + encodeURIComponent("这是一句试听") +
        "&voice=" + encodeURIComponent(voiceSel ? voiceSel.value : "") +
        "&device=" + encodeURIComponent(devSel ? devSel.value : "");
      fetch(url).then(function (r) { return r.json(); }).then(function (d) {
        btn.disabled = false;
        if (d.ok) {
          btn.textContent = label;
          btn.dataset.state = "ok";
          btn.title = "合成 " + d.synthMs + "ms · 播出 " + d.playMs + "ms · " + d.device;
          showLine("这是一句试听");
        } else {
          btn.textContent = "失败";
          btn.dataset.state = "err";
          btn.title = d.err;
        }
      }).catch(function (e) {
        btn.disabled = false;
        btn.textContent = "服务没起";
        btn.dataset.state = "err";
        btn.title = "先跑 dafeiyu -serve（默认 " + API + "）：" + e;
      });
    });
  }

  // 字幕跟着试听走一遍，让"点了→她说了"在画面上闭环。
  var cap = document.querySelector(".caption");
  var capText = cap ? cap.textContent : "";
  function showLine(t) {
    if (!cap) return;
    cap.textContent = t;
    speaking = true;
    setTimeout(function () { cap.textContent = capText; speaking = false; }, 2600);
  }

  // 输出设备从系统读，不手写：手写的名字和真设备差一个括号就匹配不上，
  // 试听就会一直"没匹配到设备"，而人只会以为按钮坏了。
  fetch(API + "/tts/devices").then(function (r) { return r.json(); }).then(function (list) {
    if (!devSel || !list || !list.length) return;
    devSel.innerHTML = "";
    var d = document.createElement("option");
    d.textContent = "系统默认（跟随 Windows 默认输出）";
    d.value = "";
    devSel.appendChild(d);
    list.forEach(function (it) {
      // 库把"系统默认"也列成 id=-1 的一项，和上面手加的占位重复，跳过。
      if (it.id < 0) return;
      var o = document.createElement("option");
      o.textContent = it.name;
      o.value = it.name;
      devSel.appendChild(o);
    });
  }).catch(function () { /* 服务没起就留着占位选项，试听按钮自己会说"服务没起" */ });

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

  var core = null, hooked = null, held = false, reads = "", speaking = false, phase = 0;

  function writeHold() {
    if (!core) return false;
    var ok = true;
    for (var k in HOLD) {
      try {
        core.setParameterValueById(k, HOLD[k]);
        // 写进去不等于按住：读回来确认，否则角标会谎报"水印已消除"。
        var got = typeof core.getParameterValueById === "function" ? core.getParameterValueById(k) : NaN;
        reads = k + "=" + got;
        if (!(got >= 0.99)) ok = false;
      } catch (e) { ok = false; }
    }
    return ok;
  }

  function apply() {
    if (!core) return;
    held = writeHold() || held;
    // 设计图里"正在说话"是假的：固定正弦当包络，只为让嘴动起来。
    phase += speaking ? 0.5 : 0.18;
    var val = speaking ? Math.abs(Math.sin(phase)) * 0.85 : 0.5 * (0.5 + 0.5 * Math.sin(phase)) * 0.5;
    try { core.setParameterValueById("ParamMouthOpenY", val); } catch (e) {}
  }

  // 不靠库的事件钩子（挂没挂上看不见就是哑的）：自己按帧写，写完读回来。
  // 库在 resize 时会换模型实例，所以每轮都重新认一次当前实例。
  function tick() {
    var oml = window.__oml;
    var mod = oml && oml.models && oml.models.model;
    var im = mod && mod.internalModel;
    var c = im && typeof im.coreModel === "function" ? im.coreModel() : (im && im.coreModel);
    if (c && c !== core) {
      core = c; hooked = im;
      try { im.on("beforeModelUpdate", apply); } catch (e) {} // 有钩子更好，没有也不靠它
    }
    apply();
    if (core && !held) {
      // 参数写不动就退回表情：表情本身就是把同一个参数置 1。
      try { mod && mod.expression && mod.expression("13 水印消除"); } catch (e) {}
      try { mod && mod.setExpression && mod.setExpression("13 水印消除"); } catch (e) {}
    }
    adopt();
  }
  setInterval(tick, 50);
  tick();

  window.__wm = function () { return { held: held, reads: reads, hasCore: !!core }; };

  setTimeout(function () {
    if (!document.querySelector("#live2d canvas")) fallback("模型没渲染出来");
    // 水印到底关掉没有，得能在画面上自证，不能只说"我写了参数"。
    var b = document.querySelector(".credit");
    if (b) b.textContent = held ? "模型：Mao绒绒家园 · " + reads
      : "模型：Mao绒绒家园 · 水印未消除 " + reads;
  }, 6000);
})();
