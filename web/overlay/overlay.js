// OBS 叠层：只读 /state，不碰声音。
//
// 声音已经由本机声卡在放，浏览器再放一遍就是双重出声。所以这里要的不是音频，
// 而是"这一刻嘴该张多大"：后端把合成好的音频压成 20Hz 响度序列送过来，
// 按 Date.now()-speakAt 取对应一格写 ParamMouthOpenY。
(function () {
  var DESIGN_W = 1080, DESIGN_H = 1920; // 设计画布：库只在这个尺寸下算得对
  var POLL_MS = 1000;      // 状态轮询周期
  var CELL_MS = 50;        // 包络一格 = 50ms（20Hz）
  var HOLD_AFTER_MS = 2500; // 说完后字幕再留一会儿，观众才来得及读完

  var st = { speaking: false, speakAt: 0, env: [], caption: "", model: "", credit: "", params: {} };
  var lastSpeakEnd = 0;
  var core = null, hooked = null;
  var mouthVal = 0;
  // 要每帧按住的参数（如关掉烤死在贴图里的水印）由后端随形象下发，页面不硬编码：
  // 换一张皮、作者换了水印参数，改的是 Go 里那张授权表，不是叠层脚本。

  function applyMouth() {
    if (!core) return;
    var hold = st.params || {};
    for (var k in hold) {
      try { core.setParameterValueById(k, hold[k]); } catch (e) {}
    }
    // 没在说话：先把嘴快速合上，然后**放手**不再写参数——
    // 否则会一直压着 idle 动作（皮本来会喘气，被我们按成死嘴）。
    if (!st.speaking || !st.speakAt) {
      if (mouthVal > 0.01) {
        mouthVal *= 0.55;
        if (mouthVal < 0.01) mouthVal = 0;
        try { core.setParameterValueById("ParamMouthOpenY", mouthVal); } catch (e) {}
        try { core.setParameterValueById("PARAM_MOUTH_OPEN_Y", mouthVal); } catch (e) {}
      }
      return;
    }
    var target = 0;
    var el = Date.now() - st.speakAt;
    if (el >= 0) {
      if (st.env && st.env.length) {
        var i = Math.floor(el / CELL_MS);
        target = i < st.env.length ? (st.env[i] || 0) / 100 : 0;
      } else {
        // 认不出音频格式时的兜底：匀速动一点，至少不是死嘴。
        target = 0.22 + 0.5 * Math.abs(Math.sin(el / 95));
      }
    }
    mouthVal = mouthVal * 0.45 + target * 0.55;
    try { core.setParameterValueById("ParamMouthOpenY", mouthVal); } catch (e) {}
    try { core.setParameterValueById("PARAM_MOUTH_OPEN_Y", mouthVal); } catch (e) {}
  }

  // 库在窗口 resize 时会 reloadModel：内部 core 换成新实例，钩子只挂一次就会挂在旧的上面
  // （表现是"刚开始嘴会动，过一会儿不动了"）。所以每 800ms 对一次当前实例。
  // 每帧自己写参数。刻意不挂 beforeModelUpdate：实测在那里写完，模型自己的更新会把值盖回去
  // （水印参数读回来一直是 0）。主仓注释声称"不会被 idle 盖回"，那是说法不是证据。
  function drive() {
    var oml = window.__oml;
    var mod = oml && oml.models && oml.models.model;
    var im = mod && mod.internalModel;
    if (im) {
      // core 每轮都要跟"当前实例"走：库在 resize 时会 reloadModel，internalModel 还是同一个对象，
      // 但里面的 core 换成新的了。认旧实例就等于把参数写进空气。
      core = typeof im.coreModel === "function" ? im.coreModel() : im.coreModel;
      hooked = im;
    }
    fitStage();
    hush();
    applyMouth();
    requestAnimationFrame(drive);
  }

  function paintCaption() {
    var el = document.getElementById("caption");
    var show = !!st.caption && (st.speaking || Date.now() - lastSpeakEnd < HOLD_AFTER_MS);
    if (show) el.textContent = st.caption;
    el.classList.toggle("show", show);
    if (!st.speaking && st.caption && !show) el.classList.remove("show");
  }

  function poll() {
    fetch("/state", { cache: "no-store" }).then(function (r) { return r.json(); }).then(function (d) {
      var wasSpeaking = st.speaking;
      st = d || st;
      if (wasSpeaking && !st.speaking) lastSpeakEnd = Date.now();
      if (st.credit) document.getElementById("credit").textContent = st.credit;
      paintCaption();
    }).catch(function () { /* 边车重启的间隙取不到状态：保持上一帧，别把嘴僵在半开 */ });
    setTimeout(poll, POLL_MS);
  }

  // 把 1080×1920 的设计画布等比缩到浏览器源的实际尺寸上，居中。
  // OBS 里挂多大都不会哑，人物比例也不会被库的 16:9 反推压扁。
  function fitStage() {
    var stg = document.getElementById("oml2d-stage");
    if (!stg) return;
    var vw = window.innerWidth || DESIGN_W;
    var vh = window.innerHeight || DESIGN_H;
    var k = Math.min(vw / DESIGN_W, vh / DESIGN_H);
    stg.style.transform = "translate(" + ((vw - DESIGN_W * k) / 2) + "px," + ((vh - DESIGN_H * k) / 2) + "px) scale(" + k + ")";
    stg.style.transformOrigin = "0 0";
  }

  // 看板娘自带的提示气泡/菜单/状态条在直播画面里是噪声。
  // 必须按 id 和 class 两种都按住：实测气泡节点是 id="oml2d-tips"、class 为空，
  // 只写 [class*=...] 选择器会整个漏掉（就出现在 OBS 截图里那个蓝气泡）。
  function hush() {
    var sel = "[id*='oml2d-tip'],[id*='oml2d-menu'],[id*='oml2d-status'],[class*='oml2d-tips'],[class*='-tips'],[class*='oml2d-status'],[class*='oml2d-menu'],[class*='oml2d-loading'],[class*='oml2d-icon']";
    document.querySelectorAll(sel).forEach(function (el) {
      el.style.setProperty("display", "none", "important");
    });
  }

  function boot() {
    if (!window.OML2D || !window.OML2D.loadOml2d) return;
    fetch("/state", { cache: "no-store" }).then(function (r) { return r.json(); }).then(function (d) {
      st = d || st;
      if (st.credit) document.getElementById("credit").textContent = st.credit;
      var name = encodeURIComponent(st.model || "大肥鱼");
      // 舞台固定按 1080×1920 出。实测库在小于 1080 宽的舞台上会算出 0x0 画布、
      // 连模型文件都不去取（720×1280、540×960 都哑掉）；OBS 浏览器源多大都由 CSS 缩放兜。
      var w = DESIGN_W, h = DESIGN_H;
      window.__oml = window.OML2D.loadOml2d({
        models: [{
          path: "/model/" + name + "/" + name + ".model3.json",
          scale: 0.4,
          position: [0, 0],
          stageStyle: { width: w + "px", height: h + "px" }
        }],
        menus: { disable: true }, tips: { disable: true }, statusBar: { disable: true }
      });
      requestAnimationFrame(drive);
      poll();
    });
  }

  // 库在视口宽度 < 1080 时会把画布算成 0x0，连模型文件都不去取（实测 540/720 哑、1080 正常，
  // 与高度无关）。竖屏直播的 1080×1920 正好满足，但挂错尺寸时必须是"画面上看得见的原因"，
  // 不能是一片空白让人以为边车没起。
  function selfCheck() {
    // 判"皮到底画出来了没有"只看画布尺寸：叠层的舞台挂在 body 上（不像后台预览会搬进侧栏），
    // 拿 #live2d canvas 当条件会让正常页面也报"宽度不够"——说谎的指示比没有更糟。
    var cv = document.querySelector("canvas");
    if (cv && cv.width > 0) return;
    var msg = "浏览器源宽度需 ≥ 1080（当前 " + window.innerWidth + "×" + window.innerHeight + "）";
    console.error("[overlay] " + msg);
    var el = document.getElementById("warn");
    if (el) el.textContent = msg;
  }

  boot();
  setTimeout(selfCheck, 6000);
})();
