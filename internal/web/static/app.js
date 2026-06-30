"use strict";

function statusLabel(s) { return t("status_" + s) || s; }
function actionLabel(a) { return t("action_" + a) || a; }

let libraries = [];
// Folder review cards are collapsed by default; track the ones the user has
// expanded so the state survives re-renders.
const expandedItems = new Set();
// Items where the user explicitly opened the manual target picker even though
// the AI already resolved a destination (override). Survives re-renders.
const manualTargetItems = new Set();

function fmtSize(n) {
	if (!n) return "";
	const u = ["B", "KB", "MB", "GB", "TB"];
	let i = 0;
	while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
	return `${n.toFixed(i ? 1 : 0)} ${u[i]}`;
}

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

// Clicking the logo / app name returns to the review queue (home).
document.getElementById("brandHome").addEventListener("click", () => {
	const reviewTab = document.querySelector('.tab[data-tab="review"]');
	if (reviewTab) reviewTab.click();
});

// ---- Items ----
async function loadItems() {
	const items = await api("GET", "/items");
	const review = items.filter((i) => i.status === "pending_review" || i.status === "error");
	const history = items.filter((i) => !["pending_review", "error"].includes(i.status));

	document.getElementById("reviewCount").textContent = review.length || "";

	const reviewList = document.getElementById("reviewList");
	reviewList.innerHTML = "";
	if (review.length === 0) reviewList.appendChild(el("p", { class: "hint", text: t("empty_review") }));
	review.forEach((i) => reviewList.appendChild(reviewCard(i)));

	const historyList = document.getElementById("historyList");
	historyList.innerHTML = "";
	if (history.length === 0) historyList.appendChild(el("p", { class: "hint", text: t("empty_history") }));
	history.forEach((i) => historyList.appendChild(historyCard(i)));
}

function fileRows(item, interactive) {
	const box = el("div", { class: "files" });
	(item.files || []).slice(0, 100).forEach((f) => {
		const action = f.action || "keep";
		const isEmpty = !f.rel_path;
		// Show only the file name, not the repeated folder path.
		const name = isEmpty ? t("empty_folder") : f.rel_path.split("/").pop();
		const pct = f.probability ? Math.round(f.probability * 100) + "%" : "";
		const prob = pct ? el("span", { class: "fprob " + probClass(f.probability), text: pct }) : null;
		const meta = el("div", { class: "frow-meta" }, [
			el("span", { class: "fname", text: name, title: name }),
			(!isEmpty && f.size) ? el("span", { class: "fsize", text: fmtSize(f.size) }) : null,
			prob,
			f.conflict ? el("span", { class: "fbadge conflict", text: t("conflict_badge") }) : null,
			f.done ? el("span", { class: "fdone", text: t("done") }) : null,
		]);
		// When the file will move, show its destination FOLDER under the file name.
		let targetEl = null;
		if (action === "move") {
			const dir = f.target_path ? f.target_path.substring(0, f.target_path.lastIndexOf("/")) : "";
			targetEl = dir
				? el("div", { class: "frow-target", text: "→ " + dir })
				: el("div", { class: "frow-target none", text: t("no_target") });
		}
		// Action toggles coloured by the AI decision: green=move, red=delete,
		// yellow=review (unsure). Nothing runs here — execution is on "Apply".
		let acts = null;
		if (interactive && !f.done) {
			const btns = [];
			if (!isEmpty) {
				const rev = el("button", { class: "fbtn review" + (action === "keep" ? " on" : ""), text: t("btn_review") });
				rev.addEventListener("click", () => setFileAction(item, f.rel_path, "keep"));
				const mv = el("button", { class: "fbtn move" + (action === "move" ? " on" : ""), text: t("btn_move") });
				mv.addEventListener("click", () => setFileAction(item, f.rel_path, "move"));
				btns.push(rev, mv);
			}
			const del = el("button", { class: "fbtn delete" + (action === "delete" ? " on" : ""), text: t("btn_delete") });
			del.addEventListener("click", () => setFileAction(item, f.rel_path, "delete"));
			btns.push(del);
			acts = el("div", { class: "frow-acts" }, btns);
		}
		const info = el("div", { class: "frow-info" }, [meta, targetEl]);
		box.appendChild(el("div", { class: "frow" }, [info, acts]));
		if (interactive && f.conflict) box.appendChild(conflictBlock(item, f));
	});
	return box;
}

// conflictBlock renders a side-by-side comparison of the file about to be moved
// (the new release) and the existing file already in the target, with one-click
// choices to replace it or keep the existing one.
function conflictBlock(item, f) {
	const c = f.conflict;
	const newName = f.rel_path ? f.rel_path.split("/").pop() : "";
	const qLine = (size, quality) => {
		const parts = [];
		if (size) parts.push(fmtSize(size));
		if (quality) parts.push(quality);
		return parts.join(" · ");
	};
	const col = (cls, label, name, size, quality) => el("div", { class: "cf-col " + cls }, [
		el("div", { class: "cf-label", text: label }),
		el("div", { class: "cf-name", text: name, title: name }),
		el("div", { class: "cf-meta", text: qLine(size, quality) }),
	]);
	const replaceBtn = el("button", { class: "btn small", text: t("conflict_replace") });
	replaceBtn.addEventListener("click", () => resolveConflict(item, f.rel_path, "replace"));
	const keepBtn = el("button", { class: "btn small secondary", text: t("conflict_keep") });
	keepBtn.addEventListener("click", () => resolveConflict(item, f.rel_path, "keep"));
	return el("div", { class: "frow-conflict" }, [
		el("div", { class: "cf-head", text: "⚠ " + t("conflict_title") }),
		el("div", { class: "cf-hint", text: t("conflict_hint") }),
		el("div", { class: "cf-cols" }, [
			col("new", t("conflict_new"), newName, f.size, c.incoming_quality),
			col("existing", t("conflict_existing"), c.existing_name, c.existing_size, c.existing_quality),
		]),
		el("div", { class: "cf-acts" }, [replaceBtn, keepBtn]),
	]);
}

// resolveConflict records the user's keep/replace decision for a colliding file.
async function resolveConflict(item, relPath, resolution) {
	try {
		await api("POST", `/items/${item.id}/conflict`, { rel_path: relPath, resolution });
		toast(t("conflict_resolved"));
		refreshAll();
	} catch (e) { toast(e.message, true); }
}

// setFileAction stores the planned action for a file (no filesystem change).
async function setFileAction(item, relPath, action) {
	try {
		await api("POST", `/items/${item.id}/file-plan`, { rel_path: relPath, action });
		refreshAll();
	} catch (e) { toast(e.message, true); }
}

function reviewCard(item) {
	const collapsed = !expandedItems.has(item.id);
	const prob = el("span", { class: "prob " + probClass(item.probability), text: Math.round(item.probability * 100) + "%" });
	const fileCount = (item.files || []).filter((f) => f.rel_path).length;
	const countEl = el("span", { class: "file-count", text: fileCount + " " + (fileCount === 1 ? t("file_one") : t("file_many")) });
	const head = el("div", { class: "card-head collapsible", title: t("collapse_hint") }, [
		el("span", { class: "caret", text: "▾" }),
		el("div", { class: "card-title", text: item.name, title: item.name }),
		countEl,
		prob,
	]);
	const errBox = item.error_message ? el("div", { class: "card-sub err", text: t("error") + ": " + item.error_message }) : null;

	const files = item.files || [];
	const hasRealFiles = files.some((f) => f.rel_path);
	const needsTarget = files.some((f) => f.action === "move" && !f.target_path && !f.done);
	const hasWork = files.some((f) => (f.action === "move" || f.action === "delete") && !f.done);
	const hasConflict = files.some((f) => f.action === "move" && f.conflict && !f.done);
	// The library picker stays hidden until the user clicks "Set target manually".
	const wantsManual = manualTargetItems.has(item.id);
	const showTargetPicker = hasRealFiles && wantsManual;
	const children = [head, errBox, fileRows(item, true)];

	// Card actions, left -> right: Apply, Re-check, Reject, Set target manually.
	const applyBtn = el("button", { class: "btn small", text: t("apply_plan") });
	applyBtn.disabled = !hasWork || needsTarget || hasConflict || dryRunActive;
	if (dryRunActive) applyBtn.title = t("whatif_active");
	applyBtn.addEventListener("click", async () => {
		applyBtn.disabled = true;
		applyBtn.classList.add("loading");
		applyBtn.textContent = t("applying");
		loadStatus();
		try {
			await api("POST", `/items/${item.id}/confirm`);
			toast(t("applied"));
			refreshAll();
		} catch (e) {
			toast(e.message, true);
			applyBtn.disabled = false;
			applyBtn.classList.remove("loading");
			applyBtn.textContent = t("apply_plan");
		}
	});
	const reBtn = el("button", { class: "btn small secondary", text: t("reanalyze") });
	reBtn.addEventListener("click", async () => {
		reBtn.disabled = true;
		reBtn.classList.add("loading");
		reBtn.textContent = t("analyzing");
		toast(t("analyzing"));
		try {
			await api("POST", `/items/${item.id}/reclassify`);
			toast(t("reanalyzed"));
			refreshAll();
		} catch (e) {
			toast(e.message, true);
			reBtn.disabled = false;
			reBtn.classList.remove("loading");
			reBtn.textContent = t("reanalyze");
		}
	});
	const rejectBtn = el("button", { class: "btn small secondary", text: t("reject") });
	rejectBtn.addEventListener("click", async () => {
		try { await api("POST", `/items/${item.id}/reject`); refreshAll(); }
		catch (e) { toast(e.message, true); }
	});
	const cardActions = [applyBtn, reBtn, rejectBtn];
	if (hasRealFiles) {
		// Toggle the manual library picker below.
		const manualBtn = el("button", { class: "btn small secondary" + (wantsManual ? " active" : ""), text: t("manual_target") });
		manualBtn.addEventListener("click", () => {
			if (manualTargetItems.has(item.id)) manualTargetItems.delete(item.id);
			else manualTargetItems.add(item.id);
			refreshAll();
		});
		cardActions.push(manualBtn);
	}
	children.push(el("div", { class: "card-actions" }, cardActions));

	// Manual target picker — revealed by "Set target manually"; collapses again
	// once a target has been set or a folder created.
	if (showTargetPicker) {
		const libSelect = el("select");
		libSelect.appendChild(el("option", { value: "", text: t("choose_lib") }));
		libraries.forEach((l) => libSelect.appendChild(el("option", { value: String(l.id), text: `${l.name} (${l.kind})` })));
		const subSelect = el("select", { style: "display:none" });
		const applyTarget = async (subFolder) => {
			const libId = parseInt(libSelect.value, 10);
			if (!libId) return;
			try {
				await api("POST", `/items/${item.id}/target`, { library_id: libId, sub_folder: subFolder || "" });
				toast(t("target_set")); refreshAll();
			} catch (e) { toast(e.message, true); }
		};
		// Load a series library's existing sub-folders into the second dropdown.
		const loadSub = async (lib) => {
			subSelect.innerHTML = "";
			subSelect.style.display = lib && lib.kind === "series" ? "" : "none";
			if (lib && lib.kind === "series") {
				const folders = await api("GET", `/libraries/${lib.id}/folders`).catch(() => []);
				subSelect.appendChild(el("option", { value: "", text: t("choose_series") }));
				folders.forEach((f) => subSelect.appendChild(el("option", { value: f, text: f })));
			}
		};
		libSelect.addEventListener("change", async () => {
			const lib = libraries.find((l) => String(l.id) === libSelect.value);
			await loadSub(lib);
			// A non-series library has no sub-folder to choose, so selecting it sets
			// the target (library root) immediately — no extra button click.
			if (lib && lib.kind !== "series") applyTarget("");
		});
		// Picking an existing series folder applies the target immediately too.
		subSelect.addEventListener("change", () => { if (subSelect.value) applyTarget(subSelect.value); });
		// Pre-select the current/suggested library and load its folders WITHOUT
		// applying, so merely rendering the card never sets a target.
		const preLib = item.target_library_id || item.suggested_library_id;
		if (preLib && libraries.some((l) => l.id === preLib)) {
			libSelect.value = String(preLib);
			loadSub(libraries.find((l) => l.id === preLib));
		}
		children.push(el("div", { class: "card-actions" }, [
			el("span", { class: "picker-label", text: t("manual_target_label") }),
			libSelect, subSelect,
		]));

		// Second row: create a NEW folder under the selected library. Pre-filled
		// with the AI suggestion when there is one.
		const newFolder = el("input", { type: "text", class: "newfolder", "data-i18n-ph": "new_folder_ph", placeholder: t("new_folder_ph") });
		if (item.suggested_folder) newFolder.value = item.suggested_folder;
		const createBtn = el("button", { class: "btn small", text: t("create_folder") });
		createBtn.addEventListener("click", async () => {
			const libId = parseInt(libSelect.value, 10);
			if (!libId) return toast(t("need_lib"), true);
			const folder = newFolder.value.trim();
			if (!folder) return toast(t("need_folder"), true);
			try {
				await api("POST", `/items/${item.id}/create-folder`, { library_id: libId, folder });
				manualTargetItems.delete(item.id);
				toast(t("folder_created")); refreshAll();
			} catch (e) { toast(e.message, true); }
		});
		children.push(el("div", { class: "card-actions newfolder-row" }, [
			el("span", { class: "picker-label", text: t("new_folder_label") }),
			newFolder, createBtn,
		]));
	}

	const card = el("div", { class: "card" + (collapsed ? " collapsed" : "") }, children);
	head.addEventListener("click", () => {
		const nowCollapsed = card.classList.toggle("collapsed");
		if (nowCollapsed) expandedItems.delete(item.id);
		else expandedItems.add(item.id);
	});
	return card;
}

function historyCard(item) {
	const prob = el("span", { class: "prob " + probClass(item.probability), text: Math.round(item.probability * 100) + "%" });
	const head = el("div", { class: "card-head" }, [
		el("div", { class: "card-title", text: item.name, title: item.name }),
		el("span", { class: "status", text: statusLabel(item.status) }),
	]);
	const target = item.target_path ? el("div", { class: "card-sub", text: "→ " + item.target_path }) : null;
	const meta = el("div", { class: "card-sub" }, [prob, " · " + (item.detected_type || "?")]);

	const delBtn = el("button", { class: "btn small secondary", text: t("remove_entry") });
	delBtn.addEventListener("click", async () => {
		try {
			await api("DELETE", `/items/${item.id}`);
			refreshAll();
		} catch (e) {
			toast(e.message, true);
		}
	});

	return el("div", { class: "card" }, [head, meta, target, fileRows(item, false), el("div", { class: "card-actions" }, [delBtn])]);
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
			toast(t("desc_saved"));
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
		const del = el("button", { class: "btn small danger", text: t("remove") });
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
	document.getElementById("addSourceBtn").textContent = (sources && sources.length) ? t("source_change") : t("source_add");
}

document.getElementById("addSourceBtn").addEventListener("click", () => openPicker("source"));

// ---- Libraries ----
async function loadLibraries() {
	await loadFolderNotes();
	libraries = (await api("GET", "/libraries")) || [];
	const list = document.getElementById("libraryList");
	list.innerHTML = "";
	libraries.forEach((l) => {
		const del = el("button", { class: "btn small danger", text: t("remove") });
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
let dryRunActive = false;
async function loadSettings() {
	const s = await api("GET", "/settings");
	document.getElementById("aiBaseUrl").value = s.ai_base_url || "";
	document.getElementById("aiModel").value = s.ai_model || "";
	document.getElementById("aiApiVersion").value = s.ai_api_version || "";
	document.getElementById("threshold").value = Math.round((s.threshold ?? 0.9) * 100);
	document.getElementById("thresholdValue").textContent = Math.round((s.threshold ?? 0.9) * 100) + "%";
	document.getElementById("autoMove").checked = !!s.auto_move;
	document.getElementById("aiContext").value = (s.ai_context || "");
	document.getElementById("ignorePatterns").value = (s.ignore_patterns || "");
	document.getElementById("keyHint").textContent = s.has_api_key ? t("key_saved") : t("key_unset");
	applyDryRun(!!s.dry_run);
}

function applyDryRun(enabled) {
	dryRunActive = enabled;
	document.getElementById("dryRun").checked = enabled;
	document.getElementById("dryRunBadge").hidden = !enabled;
}

document.getElementById("dryRun").addEventListener("change", async (e) => {
	const enabled = e.target.checked;
	try {
		await api("PUT", "/dry-run", { enabled });
		applyDryRun(enabled);
		toast(enabled ? t("whatif_on") : t("whatif_off"));
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
		ai_context: document.getElementById("aiContext").value,
		ignore_patterns: document.getElementById("ignorePatterns").value,
	};
	const key = document.getElementById("aiApiKey").value;
	if (key) body.ai_api_key = key;
	try {
		await api("PUT", "/settings", body);
		if (key) {
			document.getElementById("aiApiKey").value = "";
			document.getElementById("keyHint").textContent = t("key_saved");
		}
		toast(t("saved"));
	} catch (err) {
		toast(err.message, true);
	}
}

let saveTimer;
function autoSave() {
	clearTimeout(saveTimer);
	saveTimer = setTimeout(saveSettings, 600);
}

["aiBaseUrl", "aiModel", "aiApiVersion", "aiContext", "ignorePatterns"].forEach((id) =>
	document.getElementById(id).addEventListener("input", autoSave));
document.getElementById("aiApiKey").addEventListener("change", saveSettings);
document.getElementById("autoMove").addEventListener("change", saveSettings);
document.getElementById("threshold").addEventListener("change", saveSettings);

// ---- Scan ----
document.getElementById("scanBtn").addEventListener("click", async () => {
	try {
		await api("POST", "/scan");
		toast(t("scan_started"));
		loadStatus();
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

// Change the log level from either the Logs tab or the Settings page, keeping
// both dropdowns in sync.
async function setLogLevel(level) {
	try {
		await api("PUT", "/log-level", { level });
		const ls = document.getElementById("logLevel");
		const ss = document.getElementById("logLevelSettings");
		if (ls) ls.value = level;
		if (ss) ss.value = level;
		toast(t("log_level_set") + " " + level.toUpperCase());
		if (document.getElementById("logs").classList.contains("active")) loadLogs();
	} catch (err) { toast(err.message, true); }
}
document.getElementById("logLevel").addEventListener("change", (e) => setLogLevel(e.target.value));
document.getElementById("logLevelSettings").addEventListener("change", (e) => setLogLevel(e.target.value));
document.getElementById("logClear").addEventListener("click", () => {
	document.getElementById("logOutput").textContent = "";
});
setInterval(() => { if (document.getElementById("logs").classList.contains("active")) loadLogs(); }, 3000);

async function refreshAll() {
	await loadLibraries();
	await loadItems();
}

async function loadVersion() {
	try {
		const v = await api("GET", "/version");
		document.getElementById("aboutVersion").textContent = v.version + (v.channel && v.channel !== "local" ? ` (${v.channel})` : "");
		document.getElementById("aboutCommit").textContent = v.commit || "–";
		document.getElementById("aboutDate").textContent = v.date || "–";
		document.getElementById("aboutGo").textContent = v.go_version || "–";
		document.getElementById("footerVersion").textContent = v.version || "dev";
	} catch (_) { /* ignore */ }
}

// ---- Scan status (header) ----
let scanWasActive = false;
function fmtEta(s) {
	if (s <= 0) return "";
	if (s < 60) return `${s}s`;
	return `${Math.floor(s / 60)}m ${s % 60}s`;
}
async function loadStatus() {
	try {
		const p = await api("GET", "/status");
		const phaseEl = document.getElementById("scanPhase");
		const curEl = document.getElementById("scanCurrent");
		const bar = document.getElementById("scanBar");
		const meta = document.getElementById("scanMeta");
		if (p.active) {
			let label = t("phase_scanning");
			if (p.phase === "classifying") label = t("phase_classifying");
			else if (p.phase === "moving") label = t("phase_moving");
			const withProgress = p.phase === "classifying" || p.phase === "moving";
			phaseEl.textContent = label;
			phaseEl.className = "scan-phase busy";
			curEl.textContent = (withProgress && p.current) ? "· " + p.current : "";
			bar.hidden = !withProgress || !p.total;
			document.getElementById("scanBarFill").style.width = (p.percent || 0) + "%";
			const eta = p.eta_seconds ? ` · ${t("scan_eta")} ${fmtEta(p.eta_seconds)}` : "";
			meta.textContent = (withProgress && p.total) ? `${p.done}/${p.total} · ${p.percent || 0}%${eta}` : "";
			scanWasActive = true;
		} else {
			phaseEl.textContent = t("phase_idle");
			phaseEl.className = "scan-phase idle";
			curEl.textContent = "";
			bar.hidden = true;
			meta.textContent = "";
			if (scanWasActive) { scanWasActive = false; loadItems(); }
		}
		// Filesystem-operations health label.
		const fsLabel = document.getElementById("fsLabel");
		fsLabel.classList.toggle("ok", !!p.fs_writable);
		fsLabel.classList.toggle("bad", !p.fs_writable);
		fsLabel.title = p.fs_message || (p.fs_writable ? t("fs_ok") : t("fs_bad"));
		document.getElementById("fsText").textContent = p.fs_writable ? t("fs_ok") : t("fs_bad");
	} catch (_) { /* ignore */ }
}

// ---- Folder picker modal (sources & libraries) ----
let pickerMode = "source";
let pickerCurrent = "";
let pickerParent = "";

async function openPicker(mode) {
	pickerMode = mode;
	document.getElementById("pickerTitle").textContent =
		mode === "library" ? t("picker_lib") : t("picker_source");
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
	if (data.entries.length === 0) list.appendChild(el("li", { class: "hint", text: t("no_subfolders") }));
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
			toast(t("source_set"));
		} else {
			const name = document.getElementById("libName").value.trim();
			const kind = document.getElementById("libKind").value;
			if (!name) return toast(t("need_name"), true);
			await api("POST", "/libraries", { name, kind, path });
			if (desc) await api("PUT", "/folder-notes", { path, description: desc });
			await loadLibraries();
			toast(t("lib_created"));
		}
		closePicker();
	} catch (err) {
		toast(err.message, true);
	}
});

async function init() {
	document.getElementById("langSelect").value = currentLang();
	document.getElementById("langSelect").addEventListener("change", (e) => {
		setLang(e.target.value);
		refreshAll();
	});
	applyI18n();
	loadVersion();
	loadStatus();
	try {
		await loadSettings();
		await loadSources();
		await loadLibraries();
		await loadItems();
		try {
			const lvl = await api("GET", "/log-level");
			const level = lvl.level || "info";
			document.getElementById("logLevel").value = level;
			document.getElementById("logLevelSettings").value = level;
		} catch (_) { /* ignore */ }
		try {
			const root = await api("GET", "/browse");
			document.getElementById("rootHint").textContent = root.path;
		} catch (_) { /* ignore */ }
	} catch (e) {
		toast(e.message, true);
	}
	setInterval(loadItems, 10000);
	setInterval(loadStatus, 1500);
}

init();
