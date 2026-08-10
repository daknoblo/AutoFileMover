// Screenshot definitions for the documentation and the demo page.
//
// Every entry produces `docs/images/<id>.png` plus one section of
// `docs/demo.md`. Adding a section to the UI therefore only means adding one
// entry here — the demo page regenerates itself.

/** Opens a top-level tab and waits for its panel to be active. */
async function openTab(page, tab) {
	await page.click(`.tab[data-tab="${tab}"]`);
	await page.waitForSelector(`#${tab}.panel.active`);
}

/** Returns the review card whose title contains name. */
function card(page, name) {
	return page.locator(".card").filter({ has: page.locator(`.card-title:has-text("${name}")`) }).first();
}

/** Expands a collapsed review card so its file rows become visible. */
async function expand(page, name) {
	const c = card(page, name);
	await c.locator(".card-head").click();
	await c.locator(".files").first().waitFor({ state: "visible" });
	return c;
}

export const intro = `AutoFileMover ships a bilingual web UI (German/English). The screenshots below
are generated automatically from a demo instance filled with sample data, so
they always show the current interface.

Run the same instance locally with \`make demo\` and open <http://127.0.0.1:8099>.`;

export const shots = [
	{
		id: "review-queue",
		title: "Review queue",
		caption:
			"Everything the AI could not resolve on its own: one card per download folder with the detected confidence and the number of files. Cards are collapsed until you open them.",
		async prepare(page) {
			await openTab(page, "review");
		},
	},
	{
		id: "review-files",
		title: "Per-file decisions",
		caption:
			"Each file is judged individually: the main feature and its subtitles are moved, sample clips, NFO files and proof images are deleted. The destination folder is shown below every file that will be moved, and each action can be overridden before the plan is applied.",
		async prepare(page) {
			await openTab(page, "review");
			return expand(page, "Nebula.Drift");
		},
	},
	{
		id: "review-conflict",
		title: "Collision with an existing file",
		caption:
			"If the target already holds the same file or the same episode, the move is held back. A side-by-side comparison shows size and the release attributes parsed from the file name, so you can replace the old copy or drop the new one.",
		async prepare(page) {
			await openTab(page, "review");
			return expand(page, "Silent.Ridge");
		},
	},
	{
		id: "review-manual-target",
		title: "Manual target & new folders",
		caption:
			"When the AI endpoint fails or no target resolves, the item stays in the queue with its error. “Choose target manually” reveals the library picker, an existing sub-folder can be selected or a new one created right there.",
		async prepare(page) {
			await openTab(page, "review");
			const c = await expand(page, "Deep.Ocean");
			await c.locator(".card-actions button", { hasText: "Choose target manually" }).click();
			await c.locator("select").first().waitFor({ state: "visible" });
			return c;
		},
	},
	{
		id: "history",
		title: "History",
		caption:
			"Automatically moved, confirmed and rejected items with their target path and the files that were processed.",
		async prepare(page) {
			await openTab(page, "history");
			await page.waitForSelector("#historyList .card");
		},
	},
	{
		id: "logs",
		title: "Logs",
		caption:
			"The structured log of the running service, filtered by level. DEBUG additionally shows the full AI requests and responses.",
		async prepare(page) {
			await openTab(page, "logs");
			await page.waitForFunction(() => document.getElementById("logOutput").textContent.length > 0);
		},
	},
	{
		id: "settings",
		title: "Settings",
		caption:
			"Source folder, target libraries with their per-title sub-folder switch and AI context, the AI endpoint, the confidence threshold, what-if mode and the ignore list — all stored in the database, no config file needed.",
		async prepare(page) {
			await openTab(page, "settings");
			await page.waitForSelector("#libraryList li");
		},
	},
	{
		id: "folder-picker",
		title: "Folder browser",
		caption:
			"Source folders and libraries are picked by browsing the mounted media root, so no path can be mistyped. Every folder can carry a description that is sent to the AI as extra context.",
		async prepare(page) {
			await openTab(page, "settings");
			await page.click("#addLibraryBtn");
			await page.waitForSelector("#pickerModal:not([hidden])");
			await page.waitForSelector("#pickerList li");
		},
		selector: ".modal-box",
		pad: 24,
	},
	{
		id: "about",
		title: "About",
		caption: "Version, commit, build date and Go version of the running build.",
		async prepare(page) {
			await openTab(page, "about");
		},
	},
];
