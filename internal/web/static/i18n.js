"use strict";

// Tiny client-side i18n: data-i18n / data-i18n-ph / data-i18n-title attributes
// are translated on load and on language switch; app.js uses t() for dynamic
// strings. German is the default; English is the fallback.
const I18N = {
	de: {
		whatif: "What-If-Modus", whatif_hint: "Im What-If-Modus werden keine Dateien verschoben.",
		whatif_banner: "What-If-Modus aktiv – es werden keine Dateien verschoben.",
		scan_now: "Jetzt scannen",
		tab_review: "Review-Queue", tab_history: "Verlauf", tab_logs: "Logs", tab_settings: "Einstellungen", tab_about: "Über",
		review_title: "Zu prüfen",
		review_hint: "Pro Ordner: jede Datei mit geplanter Aktion (verschieben/löschen) und Wahrscheinlichkeit. Einzeln ausführen oder „Plan ausführen“.",
		log_level: "Level", log_autoscroll: "Auto-Scroll", log_clear: "Ansicht leeren",
		sources_title: "Quellordner", sources_hint: "Der eine Quellordner unterhalb von", sources_hint2: "der auf neue Downloads überwacht wird.",
		source_add: "＋ Quellordner wählen", source_change: "Quellordner ändern",
		libs_title: "Ziel-Bibliotheken", libs_hint: "Zielordner für Filme, Serien und Dokumentationen – Pfad per Ordnerauswahl.", lib_add: "＋ Bibliothek anlegen",
		ai_title: "KI & Verhalten", ai_base_url: "KI Endpoint (Base URL)", ai_model: "Deployment / Modell",
		ai_api_version: "Azure API-Version", ai_api_version_hint: "(leer lassen für reine OpenAI-API)", ai_api_key: "API-Key",
		threshold: "Schwellwert für automatisches Verschieben:", auto_move: "Automatisches Verschieben aktiv",
		ignore: "Ignorierte Ordner/Dateien", ignore_hint: "(eine pro Zeile; Teiltext oder Glob, z. B. _UNPACK, sample, *.txt)",
		about_title: "Über AutoFileMover", about_desc: "Selbst gehosteter Dienst, der heruntergeladene Medien per KI klassifiziert und in die passende Bibliothek verschiebt.",
		about_version: "Version", about_commit: "Commit", about_built: "Erstellt", about_go: "Go", about_repo: "Repository auf GitHub",
		picker_title: "Ordner wählen", picker_up: "▲ Übergeordnet", picker_current: "Aktueller Ordner:", picker_name: "Name", picker_type: "Typ",
		kind_movie: "Film", kind_series: "Serie", kind_doc: "Dokumentation", picker_desc: "Beschreibung (KI-Kontext, optional)",
		cancel: "Abbrechen", picker_confirm: "Diesen Ordner verwenden", picker_lib: "Bibliothek anlegen", picker_source: "Quellordner wählen",
		status_pending_review: "Prüfen", status_auto_moved: "Automatisch verarbeitet", status_confirmed: "Bestätigt", status_moving: "Wird verarbeitet…",
		status_error: "Fehler", status_rejected: "Abgelehnt", status_skipped: "Übersprungen",
		action_move: "Verschieben", action_delete: "Löschen", action_keep: "Behalten / prüfen",
		empty_review: "Nichts zu prüfen.", empty_history: "Noch kein Verlauf.", no_subfolders: "Keine Unterordner.", done: "erledigt",
		btn_move: "Verschieben", btn_delete: "Löschen", btn_review: "Review", apply_plan: "Plan ausführen", reject: "Ablehnen", set_target: "Ziel setzen",
		remove_entry: "Eintrag entfernen", choose_lib: "— Bibliothek wählen —", choose_series: "— Serienordner —",
		t_type: "Typ", t_status: "Status", t_dest: "Ziel", saved: "Gespeichert", scan_started: "Scan gestartet",
		moved: "Verschoben", deleted: "Gelöscht", applied: "Ausgeführt", target_set: "Ziel gesetzt", need_lib: "Bitte eine Bibliothek wählen",
		need_name: "Bitte einen Namen angeben", source_set: "Quellordner gesetzt", lib_created: "Bibliothek angelegt", desc_saved: "Beschreibung gespeichert",
		remove: "Entfernen", key_saved: "(gespeichert – leer lassen zum Beibehalten)", key_unset: "(noch nicht gesetzt)",
		whatif_on: "What-If-Modus aktiviert", whatif_off: "What-If-Modus deaktiviert", confirm_delete: "endgültig löschen?",
		scan_running: "Scanne", scan_eta: "noch", empty_folder: "Leerer Ordner", no_target: "kein Ziel", error: "Fehler",
		whatif_active: "What-If aktiv", reanalyze: "KI-Abgleich", reanalyzed: "Neu klassifiziert",
		ai_context: "KI-Kontext (immer mitgesendet)", ai_context_hint: "Beschreibt der KI, worum es geht und wie Dateien behandelt werden.",
		phase_idle: "Idle", phase_scanning: "Scanne Dateisystem…", phase_classifying: "KI prüft", fs_ok: "Dateisystem schreibbar", fs_bad: "Dateisystem nicht schreibbar",
		phase_moving: "Verschiebe Dateien", applying: "Wird ausgeführt…",
		create_folder: "Ordner anlegen", folder_created: "Ordner angelegt",
		analyzing: "Analysiere…",
		log_level_setting: "Log-Level", log_level_hint: "Detailgrad der Logs (DEBUG zeigt die KI-Anfragen).", log_level_set: "Log-Level:",
		collapse_hint: "Zum Ein-/Ausklappen klicken",
		file_one: "Datei", file_many: "Dateien",
	},
	en: {
		whatif: "What-if mode", whatif_hint: "In what-if mode no files are moved.",
		whatif_banner: "What-if mode active – no files are moved.",
		scan_now: "Scan now",
		tab_review: "Review queue", tab_history: "History", tab_logs: "Logs", tab_settings: "Settings", tab_about: "About",
		review_title: "Needs review",
		review_hint: "Per folder: each file with its planned action (move/delete) and probability. Run individually or “Apply plan”.",
		log_level: "Level", log_autoscroll: "Auto-scroll", log_clear: "Clear view",
		sources_title: "Source folder", sources_hint: "The single source folder under", sources_hint2: "watched for new downloads.",
		source_add: "＋ Choose source folder", source_change: "Change source folder",
		libs_title: "Target libraries", libs_hint: "Target folders for movies, series and documentaries – pick the path via the folder browser.", lib_add: "＋ Add library",
		ai_title: "AI & behaviour", ai_base_url: "AI endpoint (base URL)", ai_model: "Deployment / model",
		ai_api_version: "Azure API version", ai_api_version_hint: "(leave empty for plain OpenAI API)", ai_api_key: "API key",
		threshold: "Threshold for automatic moves:", auto_move: "Automatic moving enabled",
		ignore: "Ignored folders/files", ignore_hint: "(one per line; substring or glob, e.g. _UNPACK, sample, *.txt)",
		about_title: "About AutoFileMover", about_desc: "Self-hosted service that classifies downloaded media via AI and moves it into the matching library.",
		about_version: "Version", about_commit: "Commit", about_built: "Built", about_go: "Go", about_repo: "Repository on GitHub",
		picker_title: "Choose folder", picker_up: "▲ Parent", picker_current: "Current folder:", picker_name: "Name", picker_type: "Type",
		kind_movie: "Movie", kind_series: "Series", kind_doc: "Documentary", picker_desc: "Description (AI context, optional)",
		cancel: "Cancel", picker_confirm: "Use this folder", picker_lib: "Add library", picker_source: "Choose source folder",
		status_pending_review: "Review", status_auto_moved: "Auto-processed", status_confirmed: "Confirmed", status_moving: "Processing…",
		status_error: "Error", status_rejected: "Rejected", status_skipped: "Skipped",
		action_move: "Move", action_delete: "Delete", action_keep: "Keep / review",
		empty_review: "Nothing to review.", empty_history: "No history yet.", no_subfolders: "No sub-folders.", done: "done",
		btn_move: "Move", btn_delete: "Delete", btn_review: "Review", apply_plan: "Apply plan", reject: "Reject", set_target: "Set target",
		remove_entry: "Remove entry", choose_lib: "— choose library —", choose_series: "— series folder —",
		t_type: "Type", t_status: "Status", t_dest: "Target", saved: "Saved", scan_started: "Scan started",
		moved: "Moved", deleted: "Deleted", applied: "Applied", target_set: "Target set", need_lib: "Please choose a library",
		need_name: "Please enter a name", source_set: "Source folder set", lib_created: "Library created", desc_saved: "Description saved",
		remove: "Remove", key_saved: "(stored – leave empty to keep)", key_unset: "(not set yet)",
		whatif_on: "What-if mode enabled", whatif_off: "What-if mode disabled", confirm_delete: "permanently delete?",
		scan_running: "Scanning", scan_eta: "ETA", empty_folder: "Empty folder", no_target: "no target", error: "Error",
		whatif_active: "What-if active", reanalyze: "Re-check with AI", reanalyzed: "Re-classified",
		ai_context: "AI context (always sent)", ai_context_hint: "Tells the AI what the files are and how to handle them.",
		phase_idle: "Idle", phase_scanning: "Scanning filesystem…", phase_classifying: "AI checking", fs_ok: "Filesystem writable", fs_bad: "Filesystem not writable",
		phase_moving: "Moving files", applying: "Applying…",
		create_folder: "Create folder", folder_created: "Folder created",
		analyzing: "Analyzing…",
		log_level_setting: "Log level", log_level_hint: "Log detail level (DEBUG shows the AI requests).", log_level_set: "Log level:",
		collapse_hint: "Click to collapse/expand",
		file_one: "file", file_many: "files",
	},
};

function currentLang() { return localStorage.getItem("afm_lang") || "de"; }
function t(key) { const l = currentLang(); return (I18N[l] && I18N[l][key]) || I18N.de[key] || key; }
function setLang(l) { localStorage.setItem("afm_lang", l); applyI18n(); }

function applyI18n() {
	const lang = currentLang();
	document.documentElement.lang = lang;
	document.querySelectorAll("[data-i18n]").forEach((e) => { e.textContent = t(e.dataset.i18n); });
	document.querySelectorAll("[data-i18n-ph]").forEach((e) => { e.placeholder = t(e.dataset.i18nPh); });
	document.querySelectorAll("[data-i18n-title]").forEach((e) => { e.title = t(e.dataset.i18nTitle); });
	const sel = document.getElementById("langSelect");
	if (sel) sel.value = lang;
}
