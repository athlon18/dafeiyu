// 后台页脚本：配置读写 + 试听 + 真·大肥鱼形象台。
// 皮与渲染库在 data/host/ 下（不入库，授权见 README），故本页面必须经 http 打开。
//
// 规矩：页面上每个可点的东西都必须有后端，且**结果要看得见**。
// 上一版 保存/闭麦/加一条 三个按钮没有处理器（点了 0 请求），那是假控件。
(function () {
  var API = "";  // 同源：页面由二进制自己提供（/admin），不再另开静态服务端口
  var MODEL = "";        // 由 /state 下发：换皮不用改页面
  var HOLD = {};         // 要每帧按住的参数，同样由 /state 下发

  var $ = function (sel) { return document.querySelector(sel); };
  var $$ = function (sel, root) { return Array.prototype.slice.call((root || document).querySelectorAll(sel)); };

  var els = {
    room: $$(".pane input")[0],
    cookie: $$(".pane input")[1],
    timbre: $$(".pane select")[0],
    device: $$(".pane select")[1],
    tbody: $(".grid tbody"),
    preview: $$(".pane button.wide")[0],
    add: $$(".pane button.wide")[1],
    mute: $(".headbtns .ghost"),
    save: $(".headbtns .primary")
  };

  // 接口通不通，页面上要有一处能看出来——不许让人靠"点了没反应"去猜。
  function setLive(text, ok) {
    $(".head .live").innerHTML = '<span class="dot ' + (ok ? "on" : "off") + '"></span>' + text;
  }

  function flash(btn, ok, msg) {
    btn.title = msg;
    btn.dataset.state = ok ? "ok" : "err";
    if (!ok) {
      var old = btn.textContent;
      btn.textContent = "失败";
      setTimeout(function () { btn.textContent = old; }, 2600);
    }
  }

  function row(moment, line, enabled) {
    var tr = document.createElement("tr");
    tr.innerHTML = '<td><input class="m" value=""></td><td><input class="l" value=""></td><td class="sw on"></td>';
    tr.querySelector(".m").value = moment || "";
    tr.querySelector(".l").value = line || "";
    if (!enabled) tr.querySelector(".sw").classList.remove("on");
    tr.querySelector(".sw").addEventListener("click", function () {
      this.classList.toggle("on");
      dirty = true;
    });
    $$("input", tr).forEach(function (i) { i.addEventListener("input", function () { dirty = true; }); });
    return tr;
  }

  function collect() {
    return {
      roomId: els.room.value.trim(),
      cookie: els.cookie.value,
      engine: "openai",
      baseUrl: "http://127.0.0.1:9880/v1",
      apiKey: "",
      model: "tts-1",
      timbre: els.timbre ? els.timbre.value : "",
      speed: 1.0,
      device: els.device ? els.device.value : "",
      rules: $$(".grid tbody tr").map(function (tr) {
        return {
          moment: tr.querySelector(".m").value.trim(),
          line: tr.querySelector(".l").value.trim(),
          enabled: tr.querySelector(".sw").classList.contains("on")
        };
      })
    };
  }

  function render(cfg) {
    els.room.value = cfg.roomId || "";
    els.cookie.value = cfg.cookie || "";
    if (cfg.timbre && els.timbre) {
      var hit = $$("option", els.timbre).some(function (o) { return o.value === cfg.timbre; });
      if (hit) els.timbre.value = cfg.timbre;
    }
    if (!cfg.rules || !cfg.rules.length) return;
    els.tbody.innerHTML = "";
    cfg.rules.forEach(function (r) { els.tbody.appendChild(row(r.moment, r.line, r.enabled)); });
  }

  var dirty = false;

  els.save.addEventListener("click", function () {
    els.save.disabled = true;
    fetch(API + "/config", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(collect())
    }).then(function (r) {
      return r.json().then(function (d) { return { code: r.status, d: d }; });
    }).then(function (x) {
      els.save.disabled = false;
      if (x.code === 200 && x.d.ok) {
        dirty = false;
        flash(els.save, true, "已存 " + x.d.rules + " 条规则");
        setLive("已存 " + x.d.rules + " 条规则", true);
      } else {
        flash(els.save, false, x.d.err || "保存失败（HTTP " + x.code + "）");
        setLive("保存失败：" + (x.d.err || "HTTP " + x.code), false);
      }
    }).catch(function (e) {
      els.save.disabled = false;
      flash(els.save, false, "服务没起：" + e);
      setLive("服务没起（先跑 dafeiyu -serve）", false);
    });
  });

  var muted = false;
  function paintMute() {
    els.mute.textContent = muted ? "已闭麦" : "闭麦";
    els.mute.dataset.state = muted ? "err" : "ok";
  }
  els.mute.addEventListener("click", function () {
    fetch(API + "/mute", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ muted: !muted })
    }).then(function (r) { return r.json(); }).then(function (d) {
      if (d.ok) { muted = !!d.muted; paintMute(); } else { flash(els.mute, false, d.err || "没改成"); }
    }).catch(function (e) { flash(els.mute, false, "服务没起：" + e); });
  });

  els.add.addEventListener("click", function () {
    var tr = row("", "", true);
    els.tbody.appendChild(tr);
    dirty = true;
    tr.querySelector(".m").focus();
  });

  els.preview.addEventListener("click", function () {
    var label = els.preview.textContent;
    els.preview.disabled = true;
    els.preview.textContent = "合成中…";
    fetch(API + "/tts/preview?text=" + encodeURIComponent("这是一句试听"))
      .then(function (r) { return r.json(); }).then(function (d) {
        els.preview.disabled = false;
        els.preview.textContent = label;
        if (d.ok) {
          flash(els.preview, true, "合成 " + d.synthMs + "ms · 播出 " + d.playMs + "ms · " + d.device);
          say("这是一句试听");
        } else {
          flash(els.preview, false, d.err || "失败");
        }
      }).catch(function (e) {
        els.preview.disabled = false;
        els.preview.textContent = label;
        flash(els.preview, false, "服务没起：" + e);
      });
  });

  var cap = $(".caption");
  var capDefault = cap ? cap.textContent : "";
  var speaking = false;
  function say(text) {
    if (!cap) return;
    cap.textContent = text;
    speaking = true;
    setTimeout(function () { cap.textContent = capDefault; speaking = false; }, 2600);
  }

  function hush() {
    $$('[class*="oml2d"]').forEach(function (el) {
      var n = (el.className || "") + " " + (el.id || "");
      if (/tips|status|menu|loading|icon/.test(n)) el.style.setProperty("display", "none", "important");
    });
  }

  function adopt() {
    var st = document.getElementById("oml2d-stage");
    if (st && st.parentElement !== $("#live2d")) $("#live2d").appendChild(st);
    hush();
  }

  function fallback(msg) {
    $("#live2d").innerHTML = '<div class="nofallback">形象没起来：' + (msg || "未知原因") + "</div>";
  }

  // 设备清单从系统读，不手写：手写的名字跟真设备差一个括号就匹配不上，试听会一直报"没匹配到设备"。
  fetch(API + "/tts/devices").then(function (r) { return r.json(); }).then(function (list) {
    if (list && list.length && els.device) {
      els.device.innerHTML = "";
      var d = document.createElement("option");
      d.textContent = "系统默认（跟随 Windows 默认输出）";
      d.value = "";
      els.device.appendChild(d);
      list.forEach(function (it) {
        if (it.id < 0) return; // "系统默认"在库里也是 id=-1 一项，和上面占位重复
        var o = document.createElement("option");
        o.textContent = it.name;
        o.value = it.name;
        els.device.appendChild(o);
      });
    }
    return fetch(API + "/config").then(function (r) { return r.json(); }).then(function (cfg) {
      render(cfg);
      muted = !!cfg.muted;
      paintMute();
      setLive("已连 " + (cfg.roomId || "未填房间号") + " · 库里 " + ((cfg.rules || []).length) + " 条规则", true);
    });
  }).catch(function () {
    setLive("服务没起：先跑 dafeiyu -serve", false);
  });

  if (!window.OML2D || !window.OML2D.loadOml2d) { fallback("oml.min.js 没加载"); return; }

  fetch("/state").then(function (r) { return r.json(); }).then(function (d) {
    var name = encodeURIComponent((d && d.model) || "大肥鱼");
    MODEL = "/model/" + name + "/" + name + ".model3.json";
    if (d && d.params) HOLD = d.params;
    try {
      window.__oml = window.OML2D.loadOml2d({
        models: [{ path: MODEL, scale: 0.4, position: [0, 0], stageStyle: { width: "372px", height: "820px" } }],
        menus: { disable: true }, tips: { disable: true }, statusBar: { disable: true }
      });
    } catch (e) { fallback(String((e && e.message) || e)); return; }
    setInterval(tick, 50);
    tick();
  });

  var core = null, hooked = null, held = false, reads = "", phase = 0;

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
    phase += speaking ? 0.5 : 0.18;
    var val = speaking ? Math.abs(Math.sin(phase)) * 0.85 : 0.25 * (0.5 + 0.5 * Math.sin(phase));
    try { core.setParameterValueById("ParamMouthOpenY", val); } catch (e) {}
  }

  // 库在 resize 时会换模型实例，所以每轮重新认一次当前实例。
  function tick() {
    var oml = window.__oml;
    var mod = oml && oml.models && oml.models.model;
    var im = mod && mod.internalModel;
    var c = im && typeof im.coreModel === "function" ? im.coreModel() : (im && im.coreModel);
    if (c && c !== core) {
      core = c;
      hooked = im;
      try { im.on("beforeModelUpdate", apply); } catch (e) {}
    }
    apply();
    if (core && !held) {
      try { mod && mod.expression && mod.expression("13 水印消除"); } catch (e) {}
      try { mod && mod.setExpression && mod.setExpression("13 水印消除"); } catch (e) {}
    }
    adopt();
  }
  window.__wm = function () { return { held: held, reads: reads, hasCore: !!core }; };

  setTimeout(function () {
    if (!document.querySelector("#live2d canvas")) fallback("模型没渲染出来");
    var b = $(".credit");
    if (b) b.textContent = held ? "模型：Mao绒绒家园 · " + reads : "模型：Mao绒绒家园 · 水印未消除 " + reads;
  }, 6000);
})();
