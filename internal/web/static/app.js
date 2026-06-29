"use strict";

const STATUS_LABELS = {
	pending_review: "Prüfen",
	auto_moved: "Automatisch verschoben",
	confirmed: "Bestätigt verschoben",
	moving: "Wird verschoben…",
	error: "Fehler",
	rejected: "Abgelehnt",
	skipped: "Übersprungen",
};

let libraries = [];

async function api(method, path, body) {
	const opts = { method, headers: {} };
	if (body !== undefined) {
		opts.headers["Content-Type"] = "application/json";
		opts.body = JSON.stringify(body);
	}
	const res = await fetch("/api" + path, opts);
	if (res.status === 204) return null;
	const data = await res.json().catch(() => null);
	if (!res.ok) throw new Error(data && data.error ? data.error : "HTTP " + res.status);
	return data;
}

function toast(msg, isError) {
	const el = document.getElementById("toast");
	el.textContent = msg;
	el.classList.toggle("error", !!isError);
	el.classList.add("show");
	setTimeout(() => el.classList.remove("show"), 3000);
}

function el(tag, attrs = {}, children = []) {
	const e = document.createElement(tag);
	for (const [k, v] of Object.entries(attrs)) {
		if (k === "class") e.className = v;
		else if (k === "text") e.textContent = v;
		else e.setAttribute(k, v);
	}
	for (const c of [].concat(children)) {
		if (c) e.appendChild(typeof c === "string" ? document.createTextNode(c) : c);
	}
	return e;
}

function probClass(p) {
	if (p >= 0.9) return "high";
	if (p >= 0.6) return "mid";
	return "low";
}

// ---- Tabs ----
document.querySelectorAll(".tab").forEach((tab) => {
	tab.addEventListener("click", () => {
		document.querySelectorAll(".tab").forEach((t) => t.classList.remove("active"));
		document.querySelectorAll(".panel").forEach((p) => p.classList.remove("active"));
		tab.classList.add("active");
		document.getElementById(tab.dataset.tab).classList.add("active");
		if (tab.dataset.tab === "logs") loadLogs();
	});
});

// ---- Items ----
async function loadItems() {
	const items = await api("GET", "/items");
	const review = items.filter((i) => i.status === "pending_review" || i.status === "error");
	const history = items.filter((i) => !["pending_review", "error"].includes(i.status));

	document.getElementById("reviewCount").textContent = review.length || "";

	const reviewList = document.getElementById("reviewList");
	reviewList.innerHTML = "";
	if (review.length === 0) reviewList.appendChild(el("p", { class: "hint", text: "Nichts zu prüfen." }));
	review.forEach((i) => reviewList.appendChild(reviewCard(i)));

	const historyList = document.getElementById("historyList");
	historyList.innerHTML = "";
	if (history.length === 0) historyList.appendChild(el("p", { class: "hint", text: "Noch kein Verlauf." }));
	history.forEach((i) => historyList.appendChild(historyCard(i)));
}

function fileList(files) {
	const box = el("div", { class: "files" });
	(files || []).slice(0, 50).forEach((f) => box.appendChild(el("div", { text: f.rel_path })));
	return box;
}

function reviewCard(item) {
	const prob = el("span", { class: "prob " + probClass(item.probability), text: Math.round(item.probability * 100) + "%" });
	const head = el("div", { class: "card-head" }, [
		el("div", { class: "card-title", text: item.name }),
		prob,
	]);

	const sub = el("div", { class: "card-sub", text:
		`Typ: ${item.detected_type || "?"} · Status: ${STATUS_LABELS[item.status] || item.status}` });
	const reason = item.reasoning ? el("div", { class: "card-sub", text: item.reasoning }) : null;
	const errBox = item.error_message ? el("div", { class: "card-sub", text: "Fehler: " + item.error_message }) : null;

	// Target library select.
	const libSelect = el("select");
	libSelect.appendChild(el("option", { value: "", text: "— Bibliothek wählen —" }));
	libraries.forEach((l) => {
		const opt = el("option", { value: String(l.id), text: `${l.name} (${l.kind})` });
		if (item.target_library_id && item.target_library_id === l.id) opt.selected = true;
		libSelect.appendChild(opt);
	});

	const subFolderSelect = el("select", { style: "display:none" });

	async function refreshSubFolders() {
		const libId = libSelect.value;
		const lib = libraries.find((l) => String(l.id) === libId);
		if (lib && lib.kind === "series") {
			subFolderSelect.innerHTML = "";
			subFolderSelect.style.display = "";
			try {
				const folders = await api("GET", `/libraries/${lib.id}/folders`);
				subFolderSelect.appendChild(el("option", { value: "", text: "— Serienordner wählen —" }));
				folders.forEach((f) => subFolderSelect.appendChild(el("option", { value: f, text: f })));
			} catch (e) {
				toast(e.message, true);
			}
		} else {
			subFolderSelect.style.display = "none";
			subFolderSelect.innerHTML = "";
		}
	}
	libSelect.addEventListener("change", refreshSubFolders);

	const confirmBtn = el("button", { class: "btn small", text: "Verschieben" });
	confirmBtn.addEventListener("click", async () => {
		const libId = parseInt(libSelect.value, 10);
		if (!libId) return toast("Bitte eine Bibliothek wählen", true);
		const lib = libraries.find((l) => l.id === libId);
		const subFolder = lib && lib.kind === "series" ? subFolderSelect.value : "";
		if (lib && lib.kind === "series" && !subFolder) return toast("Bitte einen Serienordner wählen", true);
		try {
			await api("POST", `/items/${item.id}/confirm`, { library_id: libId, sub_folder: subFolder });
			toast("Verschoben");
			refreshAll();
		} catch (e) {
			toast(e.message, true);
		}
	});

	const rejectBtn = el("button", { class: "btn small secondary", text: "Ablehnen" });
	rejectBtn.addEventListener("click", async () => {
		try {
			await api("POST", `/items/${item.id}/reject`);
			refreshAll();
		} catch (e) {
			toast(e.message, true);
		}
	});

	const actions = el("div", { class: "card-actions" }, [libSelect, subFolderSelect, confirmBtn, rejectBtn]);

	const card = el("div", { class: "card" }, [head, sub, reason, errBox, fileList(item.files), actions]);
	// Apply initial sub-folder visibility if a library was pre-selected.
	refreshSubFolders();
	return card;
}

function historyCard(item) {
	const prob = el("span", { class: "prob " + probClass(item.probability), text: Math.round(item.probability * 100) + "%" });
	const head = el("div", { class: "card-head" }, [
		el("div", { class: "card-title", text: item.name }),
		el("span", { class: "status", text: STATUS_LABELS[item.status] || item.status }),
	]);
	const target = item.target_path ? el("div", { class: "card-sub", text: "→ " + item.target_path }) : null;
	const meta = el("div", { class: "card-sub" }, [prob, " · " + (item.detected_type || "?")]);

	const delBtn = el("button", { class: "btn small secondary", text: "Eintrag entfernen" });
	delBtn.addEventListener("click", async () => {
		try {
			await api("DELETE", `/items/${item.id}`);
			refreshAll();
		} catch (e) {
			toast(e.message, true);
		}
	});

	return el("div", { class: "card" }, [head, meta, target, el("div", { class: "card-actions" }, [delBtn])]);
}

// ---- Sources ----
let folderNotes = {};

async function loadFolderNotes() {
	const notes = (await api("GET", "/folder-notes")) || [];
	folderNotes = {};
	notes.forEach((n) => { folderNotes[n.path] = n.description; });
}

function descRow(path) {
	const input = el("input", { type: "text", class: "folder-desc", placeholder: "Beschreibung als KI-Kontext…" });
	input.value = folderNotes[path] || "";
	const save = el("button", { class: "btn small", type: "button", text: "Speichern" });
	save.addEventListener("click", async () => {
		try {
			await api("PUT", "/folder-notes", { path, description: input.value.trim() });
			folderNotes[path] = input.value.trim();
			toast("Beschreibung gespeichert");
		} catch (e) {
			toast(e.message, true);
		}
	});
	return el("div", { class: "desc-row" }, [input, save]);
}

async function loadSources() {
	const sources = await api("GET", "/sources");
	const list = document.getElementById("sourceList");
	list.innerHTML = "";
	(sources || []).forEach((s) => {
		const del = el("button", { class: "btn small danger", text: "Entfernen" });
		del.addEventListener("click", async () => {
			try {
				await api("DELETE", `/sources/${s.id}`);
				loadSources();
			} catch (e) {
				toast(e.message, true);
			}
		});
		list.appendChild(el("li", {}, [el("span", { text: s.path }), del]));
	});
	document.getElementById("addSourceBtn").textContent = (sources && sources.length) ? "Quellordner ändern" : "＋ Quellordner wählen";
}

document.getElementById("addSourceBtn").addEventListener("click", () => openPicker("source"));

// ---- Libraries ----
async function loadLibraries() {
	await loadFolderNotes();
	libraries = (await api("GET", "/libraries")) || [];
	const list = document.getElementById("libraryList");
	list.innerHTML = "";
	libraries.forEach((l) => {
		const del = el("button", { class: "btn small danger", text: "Entfernen" });
		del.addEventListener("click", async () => {
			try {
				await api("DELETE", `/libraries/${l.id}`);
				loadLibraries();
			} catch (e) {
				toast(e.message, true);
			}
		});
		list.appendChild(el("li", { class: "folder-item" }, [
			el("div", { class: "folder-head" }, [
				el("span", {}, [el("strong", { text: l.name }), el("span", { class: "meta", text: ` ${l.kind} · ${l.path}` })]),
				del,
			]),
			descRow(l.path),
		]));
	});
}

document.getElementById("addLibraryBtn").addEventListener("click", () => openPicker("library"));


// ---- Settings ----
async function loadSettings() {
	const s = await api("GET", "/settings");
	document.getElementById("aiBaseUrl").value = s.ai_base_url || "";
	document.getElementById("aiModel").value = s.ai_model || "";
	document.getElementById("aiApiVersion").value = s.ai_api_version || "";
	document.getElementById("threshold").value = Math.round((s.threshold ?? 0.9) * 100);
	document.getElementById("thresholdValue").textContent = Math.round((s.threshold ?? 0.9) * 100) + "%";
	document.getElementById("autoMove").checked = !!s.auto_move;
	document.getElementById("ignorePatterns").value = (s.ignore_patterns || "");
	document.getElementById("keyHint").textContent = s.has_api_key ? "(gespeichert – leer lassen zum Beibehalten)" : "(noch nicht gesetzt)";
	applyDryRun(!!s.dry_run);
}

function applyDryRun(enabled) {
	document.getElementById("dryRun").checked = enabled;
	document.getElementById("dryRunBanner").hidden = !enabled;
}

document.getElementById("dryRun").addEventListener("change", async (e) => {
	const enabled = e.target.checked;
	try {
		await api("PUT", "/dry-run", { enabled });
		applyDryRun(enabled);
		toast(enabled ? "What-If-Modus aktiviert" : "What-If-Modus deaktiviert");
	} catch (err) {
		e.target.checked = !enabled;
		toast(err.message, true);
	}
});

document.getElementById("threshold").addEventListener("input", (e) => {
	document.getElementById("thresholdValue").textContent = e.target.value + "%";
});

async function saveSettings() {
	const body = {
		ai_base_url: document.getElementById("aiBaseUrl").value.trim(),
		ai_model: document.getElementById("aiModel").value.trim(),
		ai_api_version: document.getElementById("aiApiVersion").value.trim(),
		threshold: parseInt(document.getElementById("threshold").value, 10) / 100,
		auto_move: document.getElementById("autoMove").checked,
		ignore_patterns: document.getElementById("ignorePatterns").value,
	};
	const key = document.getElementById("aiApiKey").value;
	if (key) body.ai_api_key = key;
	try {
		await api("PUT", "/settings", body);
		if (key) {
			document.getElementById("aiApiKey").value = "";
			document.getElementById("keyHint").textContent = "(gespeichert – leer lassen zum Beibehalten)";
		}
		toast("Gespeichert");
	} catch (err) {
		toast(err.message, true);
	}
}

let saveTimer;
function autoSave() {
	clearTimeout(saveTimer);
	saveTimer = setTimeout(saveSettings, 600);
}

["aiBaseUrl", "aiModel", "aiApiVersion", "ignorePatterns"].forEach((id) =>
	document.getElementById(id).addEventListener("input", autoSave));
document.getElementById("aiApiKey").addEventListener("change", saveSettings);
document.getElementById("autoMove").addEventListener("change", saveSettings);
document.getElementById("threshold").addEventListener("change", saveSettings);

// ---- Scan ----
document.getElementById("scanBtn").addEventListener("click", async () => {
	try {
		await api("POST", "/scan");
		toast("Scan gestartet");
		setTimeout(loadItems, 1500);
	} catch (e) {
		toast(e.message, true);
	}
});

// ---- Logs ----
function fmtLog(line) {
	try {
		const o = JSON.parse(line);
		const t = (o.time || "").replace("T", " ").slice(0, 19);
		const lvl = (o.level || "").padEnd(5);
		const rest = Object.entries(o).filter(([k]) => !["time", "level", "msg"].includes(k))
			.map(([k, v]) => `${k}=${v}`).join(" ");
		return `${t} ${lvl} ${o.msg || ""}${rest ? "  " + rest : ""}`;
	} catch { return line; }
}

async function loadLogs() {
	try {
		const data = await api("GET", "/logs");
		const out = document.getElementById("logOutput");
		out.textContent = (data.lines || []).map(fmtLog).join("\n");
		if (document.getElementById("logAutoScroll").checked) out.scrollTop = out.scrollHeight;
	} catch (e) { /* ignore */ }
}

document.getElementById("logLevel").addEventListener("change", async (e) => {
	try {
		await api("PUT", "/log-level", { level: e.target.value });
		toast("Log-Level: " + e.target.value.toUpperCase());
		loadLogs();
	} catch (err) { toast(err.message, true); }
});
document.getElementById("logClear").addEventListener("click", () => {
	document.getElementById("logOutput").textContent = "";
});
setInterval(() => { if (document.getElementById("logs").classList.contains("active")) loadLogs(); }, 3000);

async function refreshAll() {
	await loadLibraries();
	await loadItems();
}

// ---- Folder picker modal (sources & libraries) ----
let pickerMode = "source";
let pickerCurrent = "";
let pickerParent = "";

async function openPicker(mode) {
	pickerMode = mode;
	document.getElementById("pickerTitle").textContent =
		mode === "library" ? "Bibliothek anlegen" : "Quellordner wählen";
	document.getElementById("libFields").hidden = mode !== "library";
	document.getElementById("pickerDescLabel").hidden = mode !== "library";
	document.getElementById("pickerDesc").value = "";
	document.getElementById("libName").value = "";
	document.getElementById("libKind").value = "movie";
	document.getElementById("pickerModal").hidden = false;
	await pickerLoad("");
}

function closePicker() {
	document.getElementById("pickerModal").hidden = true;
}

async function pickerLoad(path) {
	const q = path ? "?path=" + encodeURIComponent(path) : "";
	const data = await api("GET", "/browse" + q);
	pickerCurrent = data.path;
	pickerParent = data.parent;
	document.getElementById("pickerPath").textContent = data.path;
	document.getElementById("pickerSelectedPath").textContent = data.path;
	document.getElementById("pickerUp").disabled = data.at_root;
	if (pickerMode === "library") {
		const base = data.path.split("/").filter(Boolean).pop() || "";
		document.getElementById("libName").value = base;
	}
	const list = document.getElementById("pickerList");
	list.innerHTML = "";
	if (data.entries.length === 0) list.appendChild(el("li", { class: "hint", text: "Keine Unterordner." }));
	data.entries.forEach((entry) => {
		const open = el("button", { class: "btn small secondary folder-open", type: "button", text: "📁 " + entry.name });
		open.addEventListener("click", () => pickerLoad(entry.path));
		list.appendChild(el("li", { class: "browse-item" }, [open]));
	});
}

document.getElementById("pickerUp").addEventListener("click", () => { if (pickerParent) pickerLoad(pickerParent); });
document.getElementById("pickerCancel").addEventListener("click", closePicker);

document.getElementById("pickerConfirm").addEventListener("click", async () => {
	const path = pickerCurrent;
	const desc = document.getElementById("pickerDesc").value.trim();
	try {
		if (pickerMode === "source") {
			const existing = (await api("GET", "/sources")) || [];
			for (const s of existing) await api("DELETE", `/sources/${s.id}`);
			await api("POST", "/sources", { path });
			await loadSources();
			toast("Quellordner gesetzt");
		} else {
			const name = document.getElementById("libName").value.trim();
			const kind = document.getElementById("libKind").value;
			if (!name) return toast("Bitte einen Namen angeben", true);
			await api("POST", "/libraries", { name, kind, path });
			if (desc) await api("PUT", "/folder-notes", { path, description: desc });
			await loadLibraries();
			toast("Bibliothek angelegt");
		}
		closePicker();
	} catch (err) {
		toast(err.message, true);
	}
});

async function init() {
	try {
		await loadSettings();
		await loadSources();
		await loadLibraries();
		await loadItems();
		try {
			const lvl = await api("GET", "/log-level");
			document.getElementById("logLevel").value = lvl.level || "info";
		} catch (_) { /* ignore */ }
		try {
			const root = await api("GET", "/browse");
			document.getElementById("rootHint").textContent = root.path;
		} catch (_) { /* ignore */ }
	} catch (e) {
		toast(e.message, true);
	}
	setInterval(loadItems, 10000);
}

init();
