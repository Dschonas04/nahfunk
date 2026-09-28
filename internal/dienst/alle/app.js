// The user interface polls the local daemon; all the real work happens there, which is why
// closing this page does not stop a transfer.
const el = (id) => document.getElementById(id);
let dateien = [];
let bekannt = {};

function groesse(n) {
  const einheiten = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1024 && i < einheiten.length - 1) { n /= 1024; i++; }
  return `${n < 10 && i > 0 ? n.toFixed(1) : Math.round(n)} ${einheiten[i]}`;
}

async function hole(weg, zusatz) {
  const antwort = await fetch(weg, zusatz);
  const text = await antwort.text();
  let wert = {};
  try { wert = text ? JSON.parse(text) : {}; } catch { wert = { fehler: text }; }
  if (!antwort.ok) throw new Error(wert.fehler || antwort.statusText);
  return wert;
}

function zeileMachen(e, seite) {
  const li = document.createElement("li");
  const kopf = document.createElement("div");
  kopf.className = "kopf";
  const name = document.createElement("b");
  name.textContent = e.name;
  const rechts = document.createElement("span");
  rechts.className = "leise";
  rechts.textContent = `${groesse(e.uebertragen || 0)} / ${groesse(e.groesse || 0)}`;
  kopf.append(name, rechts);

  const balken = document.createElement("div");
  balken.className = "balken";
  const innen = document.createElement("div");
  innen.style.width = `${Math.min(100, Math.round(100 * (e.groesse ? (e.uebertragen || 0) / e.groesse : 0)))}%`;
  balken.append(innen);

  const zustand = document.createElement("div");
  zustand.className = "zustand" + (e.zustand === "fehler" ? " fehler" : "");
  const gegen = e.gegenseite ? ` · ${seite === "empfang" ? "von" : "an"} ${e.gegenseite}` : "";
  zustand.textContent = `${e.zustand}${gegen}${e.meldung ? " · " + e.meldung : ""}${e.versuche ? ` · Versuch ${e.versuche}` : ""}`;

  li.append(kopf, balken, zustand);

  const tasten = document.createElement("div");
  tasten.className = "tasten";
  if (seite === "empfang" && e.zustand === "freigabe") {
    tasten.append(taste("Annehmen", () => hole(`/api/freigeben?id=${e.id}`, { method: "POST" }).then(laden)));
    tasten.append(taste("Ablehnen", () => hole(`/api/freigeben?id=${e.id}&ja=nein`, { method: "POST" }).then(laden), true));
  }
  if (seite === "versand" && e.zustand === "fehler") {
    tasten.append(taste("Erneut versuchen", () => hole(`/api/erneut?id=${e.id}`, { method: "POST" }).then(laden)));
  }
  if (e.zustand === "fertig" || e.zustand === "fehler") {
    tasten.append(taste("Aus Liste entfernen", () => hole(`/api/vergessen?id=${e.id}&seite=${seite}`, { method: "POST" }).then(laden), true));
  }
  if (tasten.childElementCount) li.append(tasten);
  return li;
}

function taste(text, tun, still) {
  const b = document.createElement("button");
  b.textContent = text;
  if (still) b.className = "still";
  b.onclick = () => tun().catch((f) => melden("sendeHinweis", f.message, true));
  return b;
}

function melden(wo, text, schlimm) {
  const p = el(wo);
  p.textContent = text;
  p.className = "hinweis" + (schlimm ? " fehler" : "");
}

async function laden() {
  const z = await hole("/api/zustand");
  bekannt = z.bekannt || {};
  el("eigen").textContent = `${z.eigen.name} · Port ${z.eigen.port} · Empfang in ${z.eigen.zielordner} · Fassung ${z.eigen.version}`;
  el("code").textContent = z.eigen.code;
  if (document.activeElement !== el("name")) el("name").value = z.eigen.name;
  if (document.activeElement !== el("zielordner")) el("zielordner").value = z.eigen.zielordner;
  el("auto").checked = z.eigen.auto;

  const ziel = el("ziel");
  const vorher = ziel.value;
  ziel.innerHTML = "";
  if (!(z.geraete || []).length) {
    const leer = document.createElement("option");
    leer.textContent = "kein Gerät gefunden";
    leer.value = "";
    ziel.append(leer);
  }
  for (const g of z.geraete || []) {
    const o = document.createElement("option");
    o.value = g.id;
    o.textContent = `${g.name} (${g.adresse})`;
    ziel.append(o);
  }
  if (vorher) ziel.value = vorher;

  el("eingang").replaceChildren(...(z.empfang || []).map((e) => zeileMachen(e, "empfang")));
  el("ausgang").replaceChildren(...(z.versand || []).map((e) => zeileMachen(e, "versand")));
  if (!(z.empfang || []).length) el("eingang").innerHTML = '<li class="leise">noch nichts empfangen</li>';
  if (!(z.versand || []).length) el("ausgang").innerHTML = '<li class="leise">noch nichts gesendet</li>';
}

function dateienMerken(liste) {
  dateien = Array.from(liste);
  el("gewaehlt").textContent = dateien.length
    ? dateien.map((d) => `${d.name} (${groesse(d.size)})`).join(", ")
    : "nichts ausgewählt";
}

el("auswahl").onchange = (e) => dateienMerken(e.target.files);
const ablage = el("ablage");
["dragenter", "dragover"].forEach((art) => ablage.addEventListener(art, (e) => {
  e.preventDefault();
  ablage.classList.add("aktiv");
}));
["dragleave", "drop"].forEach((art) => ablage.addEventListener(art, () => ablage.classList.remove("aktiv")));
ablage.addEventListener("drop", (e) => {
  e.preventDefault();
  dateienMerken(e.dataTransfer.files);
});

el("suchen").onclick = async () => {
  el("suchen").disabled = true;
  melden("sendeHinweis", "suche im Netzwerk …");
  try { await hole("/api/suchen", { method: "POST" }); await laden(); melden("sendeHinweis", ""); }
  catch (f) { melden("sendeHinweis", f.message, true); }
  finally { el("suchen").disabled = false; }
};

el("senden").onclick = async () => {
  const zielId = el("ziel").value;
  if (!zielId) return melden("sendeHinweis", "Erst ein Gerät suchen und auswählen.", true);
  if (!dateien.length) return melden("sendeHinweis", "Erst Dateien auswählen.", true);
  let code = bekannt[zielId] || "";
  if (!code) {
    code = prompt("Freigabecode des Zielgeräts (steht dort oben rechts):") || "";
    if (!code) return melden("sendeHinweis", "Ohne Code nimmt das andere Gerät nichts an.", true);
  }
  const form = new FormData();
  form.append("geraet", zielId);
  form.append("code", code);
  for (const d of dateien) form.append("dateien", d, d.name);
  el("senden").disabled = true;
  melden("sendeHinweis", "übergebe an den Dienst …");
  try {
    const raus = await hole("/api/senden", { method: "POST", body: form });
    melden("sendeHinweis", `${raus.eingereiht.length} Datei(en) eingereiht — der Dienst überträgt weiter, auch wenn du dieses Fenster schließt.`);
    dateienMerken([]);
    el("auswahl").value = "";
    await laden();
  } catch (f) {
    melden("sendeHinweis", f.message, true);
  } finally {
    el("senden").disabled = false;
  }
};

el("speichern").onclick = async () => {
  try {
    await hole("/api/einstellungen", {
      method: "POST",
      body: JSON.stringify({ name: el("name").value, zielordner: el("zielordner").value, auto: el("auto").checked }),
    });
    melden("einstHinweis", "gespeichert");
    await laden();
  } catch (f) {
    melden("einstHinweis", f.message, true);
  }
};

laden().catch((f) => melden("sendeHinweis", f.message, true));
setInterval(() => laden().catch(() => {}), 1500);
