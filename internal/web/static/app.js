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
}

document.getElementById("sourceForm").addEventListener("submit", async (e) => {
	e.preventDefault();
	const input = document.getElementById("sourcePath");
	try {
		await api("POST", "/sources", { path: input.value.trim() });
		input.value = "";
		loadSources();
		toast("Quellordner hinzugefügt");
	} catch (err) {
		toast(err.message, true);
	}
});

// ---- Libraries ----
async function loadLibraries() {
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
		list.appendChild(el("li", {}, [
			el("span", {}, [el("strong", { text: l.name }), el("span", { class: "meta", text: ` ${l.kind} · ${l.path}` })]),
			del,
		]));
	});
}

document.getElementById("libraryForm").addEventListener("submit", async (e) => {
	e.preventDefault();
	const name = document.getElementById("libName");
	const kind = document.getElementById("libKind");
	const path = document.getElementById("libPath");
	try {
		await api("POST", "/libraries", { name: name.value.trim(), kind: kind.value, path: path.value.trim() });
		name.value = "";
		path.value = "";
		loadLibraries();
		toast("Bibliothek hinzugefügt");
	} catch (err) {
		toast(err.message, true);
	}
});

// ---- Settings ----
async function loadSettings() {
	const s = await api("GET", "/settings");
	document.getElementById("aiBaseUrl").value = s.ai_base_url || "";
	document.getElementById("aiModel").value = s.ai_model || "";
	document.getElementById("aiApiVersion").value = s.ai_api_version || "";
	document.getElementById("threshold").value = Math.round((s.threshold ?? 0.9) * 100);
	document.getElementById("thresholdValue").textContent = Math.round((s.threshold ?? 0.9) * 100) + "%";
	document.getElementById("autoMove").checked = !!s.auto_move;
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

document.getElementById("settingsForm").addEventListener("submit", async (e) => {
	e.preventDefault();
	const body = {
		ai_base_url: document.getElementById("aiBaseUrl").value.trim(),
		ai_model: document.getElementById("aiModel").value.trim(),
		ai_api_version: document.getElementById("aiApiVersion").value.trim(),
		threshold: parseInt(document.getElementById("threshold").value, 10) / 100,
		auto_move: document.getElementById("autoMove").checked,
	};
	const key = document.getElementById("aiApiKey").value;
	if (key) body.ai_api_key = key;
	try {
		await api("PUT", "/settings", body);
		document.getElementById("aiApiKey").value = "";
		await loadSettings();
		toast("Einstellungen gespeichert");
	} catch (err) {
		toast(err.message, true);
	}
});

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

async function refreshAll() {
	await loadLibraries();
	await loadItems();
}

// ---- Folder browser & descriptions ----
let currentBrowsePath = "";
let currentBrowseParent = "";

async function loadBrowse(path) {
	const q = path ? "?path=" + encodeURIComponent(path) : "";
	const data = await api("GET", "/browse" + q);
	currentBrowsePath = data.path;
	currentBrowseParent = data.parent;
	document.getElementById("browsePath").textContent = data.path;
	document.getElementById("browseUp").disabled = data.at_root;

	const list = document.getElementById("browseList");
	list.innerHTML = "";
	if (data.entries.length === 0) {
		list.appendChild(el("li", { class: "hint", text: "Keine Unterordner." }));
	}
	data.entries.forEach((entry) => {
		const open = el("button", { class: "btn small secondary folder-open", type: "button", text: "📁 " + entry.name });
		open.addEventListener("click", () => loadBrowse(entry.path));

		const desc = el("input", { type: "text", class: "folder-desc", placeholder: "Beschreibung als KI-Kontext…" });
		desc.value = entry.description || "";

		const save = el("button", { class: "btn small", type: "button", text: "Speichern" });
		save.addEventListener("click", async () => {
			try {
				await api("PUT", "/folder-notes", { path: entry.path, description: desc.value.trim() });
				toast("Beschreibung gespeichert");
			} catch (e) {
				toast(e.message, true);
			}
		});

		list.appendChild(el("li", { class: "browse-item" }, [open, desc, save]));
	});
}

document.getElementById("browseUp").addEventListener("click", () => {
	if (currentBrowseParent) loadBrowse(currentBrowseParent);
});

async function init() {
	try {
		await loadSettings();
		await loadSources();
		await loadLibraries();
		await loadItems();
		await loadBrowse("");
	} catch (e) {
		toast(e.message, true);
	}
	setInterval(loadItems, 10000);
}

init();
