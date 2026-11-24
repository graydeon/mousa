// Mousa live view. Every value is rendered with textContent.
(function () {
	"use strict";

	const STEP_MS = 140; // playback gap so a millisecond request is still visible
	const KEEP = 100;
	const SLOW_MS = 250;
	const LANES = [
		["query", "Search", ["receive", "admission", "access", "retrieval", "rank", "pack", "commit", "response"]],
		["note", "Remember", ["receive", "admission", "access", "citations", "note", "commit", "response"]],
		["document", "Document", ["receive", "admission", "access", "represent", "index", "commit", "response"]],
	];
	const NAMES = {
		receive: "Arrive", admission: "Log", access: "Permit", retrieval: "Find", rank: "Rank", pack: "Pack",
		citations: "Cite", note: "Write", represent: "Split", index: "Index", commit: "Save", response: "Reply",
	};
	const VERBS = { query: "Searched", note: "Saved a note", document: "Updated a document" };
	const ENDS = {
		no_op: "Already saved", denied: "Refused", invalid: "Bad request",
		failed: "Failed", canceled: "Canceled", interrupted: "Cut off",
	};

	const $ = function (id) { return document.getElementById(id); };
	const link = $("link"), linkText = $("link-text"), keyForm = $("key-form"), keyInput = $("key");
	const alertBox = $("alert"), agentList = $("agents"), log = $("log"), quiet = $("quiet");
	const pick = $("pick"), busy = $("busy");
	const detail = $("detail"), detailTitle = $("detail-title"), detailBody = $("detail-body");

	function el(tag, cls, text) {
		const node = document.createElement(tag);
		if (cls) { node.className = cls; }
		if (text !== undefined) { node.textContent = text; }
		return node;
	}
	function ms(usec) {
		const value = usec / 1000;
		if (value < 0.1) { return "<0.1 ms"; }
		if (value >= 1000) { return (value / 1000).toFixed(1) + " s"; }
		return (value >= 10 ? value.toFixed(0) : value.toFixed(1)) + " ms";
	}
	function median(values) {
		if (!values.length) { return 0; }
		const sorted = values.slice().sort(function (a, b) { return a - b; });
		return sorted[Math.floor(sorted.length / 2)];
	}
	function clock(usec) { return new Date(usec / 1000).toLocaleTimeString([], { hour12: false }); }
	function ago(usec) {
		const seconds = Math.max(0, Math.round((Date.now() - usec / 1000) / 1000));
		if (seconds < 60) { return "now"; }
		if (seconds < 3600) { return Math.round(seconds / 60) + " min"; }
		if (seconds < 86400) { return Math.round(seconds / 3600) + " h"; }
		return Math.round(seconds / 86400) + " d";
	}

	// --- pipeline -------------------------------------------------------
	const stages = {}; // "operation:stage" -> { el, note, busy, times }
	const laneTotals = {};
	LANES.forEach(function (lane) {
		const row = el("div", "lane"), list = el("ol"), total = el("span");
		lane[2].forEach(function (stage) {
			const chip = el("li", "stage"), note = el("i");
			chip.append(el("b", "", NAMES[stage]), note);
			list.appendChild(chip);
			stages[lane[0] + ":" + stage] = { el: chip, note: note, busy: 0, times: [] };
		});
		row.append(el("b", "", lane[1]), list, total);
		$("flow").appendChild(row);
		laneTotals[lane[0]] = total;
	});
	let inFlight = 0;
	function paintBusy() { busy.textContent = inFlight > 0 ? inFlight + " in flight" : "Idle"; }
	function stageStart(key) {
		const stage = stages[key];
		if (!stage) { return; }
		stage.busy++;
		stage.el.classList.remove("bad", "slow");
		stage.el.classList.add("on");
	}
	function stageEnd(key, frame) {
		const stage = stages[key];
		if (!stage) { return; }
		stage.busy = Math.max(0, stage.busy - 1);
		if (stage.busy === 0) { stage.el.classList.remove("on"); }
		if (frame.phase === "error") {
			stage.el.classList.add("bad");
			stage.note.textContent = frame.stage === "access" ? "refused" : "failed";
			return;
		}
		if (frame.duration_usec) {
			stage.times.push(frame.duration_usec);
			if (stage.times.length > 30) { stage.times.shift(); }
			stage.el.classList.toggle("slow", frame.duration_usec / 1000 > SLOW_MS);
			stage.note.textContent = ms(median(stage.times));
		}
	}
	function resetStages() {
		Object.keys(stages).forEach(function (key) {
			stages[key].busy = 0;
			stages[key].el.classList.remove("on");
		});
		inFlight = 0;
		paintBusy();
	}

	// --- requests -------------------------------------------------------
	const requests = new Map();
	const agents = new Map();
	let bearer = "", cursor = "", stream = null, retry = null, shown = null, opened = null;

	function headers(extra) {
		const out = extra || {};
		if (bearer) { out.Authorization = "Bearer " + bearer; }
		return out;
	}
	function agentName(admission) {
		if (!admission.authenticated) { return "Unknown"; }
		return (admission.client && admission.client.harness) || admission.principal;
	}
	function identify(item, admission) {
		const client = admission.client || {};
		item.known = !!admission.authenticated;
		item.who = agentName(admission);
		item.model = client.model || "";
		item.task = client.agent || "";
		item.run = client.run_id || "";
		item.key = admission.principal || "";
		item.cells[1].textContent = item.who;
	}
	function record(id, admission) {
		let item = requests.get(id);
		if (item) { return item; }
		item = {
			id: id, op: admission.operation, at: admission.received_at_usec || Date.now() * 1000,
			state: "", took: 0, stages: [], queue: [], timer: null, content: null, live: false,
			tr: el("tr", "new"), cells: [],
		};
		for (let i = 0; i < 6; i++) { item.cells.push(item.tr.appendChild(el("td"))); }
		item.cells[0].textContent = clock(item.at);
		item.cells[2].textContent = VERBS[item.op] || item.op;
		item.cells[3].className = "text";
		item.cells[4].className = "wait";
		item.cells[4].textContent = "Working";
		identify(item, admission);
		item.tr.tabIndex = 0;
		item.tr.addEventListener("click", function () { open(item); });
		item.tr.addEventListener("keydown", function (event) { if (event.key === "Enter") { open(item); } });
		requests.set(id, item);
		// Keep rows newest first whether they arrive live or from history.
		let before = log.firstChild;
		while (before && before.item.at > item.at) { before = before.nextSibling; }
		item.tr.item = item;
		log.insertBefore(item.tr, before);
		quiet.hidden = true;
		while (log.children.length > KEEP) {
			requests.delete(log.lastChild.item.id);
			log.lastChild.remove();
		}
		return item;
	}
	function resultText(item) {
		if (item.state === "committed") {
			if (item.op !== "query") { return "Saved"; }
			if (!item.content) { return "Done"; }
			const count = (item.content.returned || []).length;
			return count === 1 ? "1 passage" : count + " passages";
		}
		return ENDS[item.state] || item.state;
	}
	function paintRow(item) {
		const good = item.state === "committed" || item.state === "no_op";
		item.cells[4].className = good ? "" : "bad";
		item.cells[4].textContent = resultText(item);
		item.cells[5].textContent = item.took ? ms(item.took) : "";
		if (item.content) { item.cells[3].textContent = item.content.query || item.content.note || ""; }
	}
	function finish(item, state, tookUsec) {
		item.state = state;
		if (tookUsec > 0) { item.took = tookUsec; }
		paintRow(item);
		touchAgent(item);
		paintStats();
		wantContent(item);
		// The side panel follows the newest request, even when an older one
		// finishes its replay later.
		const newest = log.firstChild.item;
		show(newest.state ? newest : item);
		repaint(item);
	}

	// --- content: what was asked and what came back -----------------------
	const wanted = [];
	let fetching = false;
	function wantContent(item) {
		if (item.content || item.asked) { return; }
		item.asked = true;
		wanted.push(item);
		drain();
	}
	async function drain() {
		if (fetching) { return; }
		fetching = true;
		while (wanted.length) {
			const item = wanted.pop(); // newest first
			try {
				const response = await fetch("/v1/requests/" + encodeURIComponent(item.id), { headers: headers(), cache: "no-store" });
				if (response.ok) {
					const result = (await response.json()).result;
					item.content = result.content || {};
					item.evidence = result.evidence || null;
					item.outcome = result.outcome || null;
					paintRow(item);
					repaint(item);
				}
			} catch (err) { /* the row keeps its summary */ }
		}
		fetching = false;
	}

	// --- agents and stats -----------------------------------------------
	function touchAgent(item) {
		let agent = agents.get(item.who);
		if (!agent) {
			agent = { live: 0, last: 0, li: el("li"), seen: el("small"), model: el("small", "model") };
			agent.li.append(el("span", "dot"), el("b", "", item.who), agent.seen, agent.model);
			agents.set(item.who, agent);
			agentList.appendChild(agent.li);
		}
		if (item.model) { agent.model.textContent = item.model; }
		agent.last = Math.max(agent.last, item.at);
		return agent;
	}
	function paintAgents() {
		agents.forEach(function (agent) {
			agent.li.classList.toggle("on", agent.live > 0);
			agent.seen.textContent = agent.live > 0 ? "working" : ago(agent.last);
		});
	}
	function paintStats() {
		const counts = { query: 0, note: 0, document: 0 }, took = { query: [], note: [], document: [] };
		let refused = 0;
		const all = [];
		requests.forEach(function (item) {
			if (!item.state) { return; }
			if (counts[item.op] !== undefined) { counts[item.op]++; }
			if (item.state === "denied") { refused++; }
			if (item.took && took[item.op]) { took[item.op].push(item.took); all.push(item.took); }
		});
		$("stat-query").textContent = counts.query;
		$("stat-note").textContent = counts.note;
		$("stat-document").textContent = counts.document;
		$("stat-refused").textContent = refused;
		$("stat-refused").classList.toggle("some", refused > 0);
		$("stat-typical").textContent = all.length ? ms(median(all)) : "–";
		Object.keys(laneTotals).forEach(function (op) {
			laneTotals[op].textContent = took[op].length ? ms(median(took[op])) : "";
		});
		paintAgents();
	}

	// --- one request: side panel (newest) and popup (the one clicked) ------
	// The popup shows everything recorded for the request; the side panel
	// shows the short form of the same thing.
	function render(target, item, full) {
		target.textContent = "";
		const content = item.content || {};
		const words = el("div", "words"), facts = el("div", "facts");
		if (content.query) { words.append(el("h3", "", "Asked"), el("p", "said", content.query)); }
		if (content.note) { words.append(el("h3", "", "Saved"), el("p", "said", content.note)); }
		if (item.op === "query" && item.content) {
			const returned = content.returned || [];
			words.appendChild(el("h3", "", returned.length ? "Returned " + returned.length : "Returned nothing"));
			returned.forEach(function (text, index) {
				const passage = el("p", "got", text);
				if (full) { passage.dataset.rank = index + 1; }
				words.appendChild(passage);
			});
		}
		const evidence = item.evidence || {}, outcome = item.outcome || {};
		const record = evidence.id || "";
		const list = el("dl");
		[
			["Agent", item.who], ["Model", item.model], ["Task", item.task], ["Run", item.run], ["Key", item.key],
			["Did", VERBS[item.op] || item.op], ["Result", item.state ? resultText(item) : "Working"],
			["Error", full && outcome.error_code],
			["When", new Date(item.at / 1000).toLocaleString()], ["Took", item.took ? ms(item.took) : ""],
			["Source", evidence.source || (full && (evidence.sources || []).join("\n"))], ["Item", evidence.item_id],
			[evidence.kind === "trail" ? "Trail" : "Record", full ? record : record.slice(0, 16)],
			["Request", full && item.id], ["Log entry", full && outcome.seq],
		].forEach(function (fact) {
			if (fact[1]) { list.append(el("dt", "", fact[0]), el("dd", "", String(fact[1]))); }
		});
		facts.append(el("h3", "", "Request"), list);
		if (item.stages.length) {
			const longest = Math.max.apply(null, item.stages.map(function (stage) { return stage[1]; }));
			const times = el("div", "times");
			item.stages.forEach(function (stage) {
				const rail = el("span", "rail"), fill = el("span", "fill");
				fill.style.width = Math.max(2, Math.round(100 * stage[1] / longest)) + "%";
				rail.appendChild(fill);
				times.append(el("span", "", NAMES[stage[0]]), rail, el("span", "took", ms(stage[1])));
			});
			facts.append(el("h3", "", "Stages"), times);
		}
		if (words.firstChild) { target.appendChild(words); }
		target.appendChild(facts);
	}
	function show(item) {
		shown = item;
		render(pick, item, false);
	}
	function open(item) {
		if (opened) { opened.tr.classList.remove("picked"); }
		opened = item;
		item.tr.classList.add("picked");
		detailTitle.textContent = (VERBS[item.op] || item.op) + " · " + item.who + " · " + clock(item.at);
		render(detailBody, item, true);
		wantContent(item);
		if (!detail.open) { detail.showModal(); }
	}
	function repaint(item) {
		if (shown === item) { show(item); }
		if (opened === item && detail.open) { render(detailBody, item, true); }
	}
	detail.addEventListener("close", function () {
		if (opened) { opened.tr.classList.remove("picked"); }
		opened = null;
	});
	// A click on the dimmed page outside the box closes it.
	detail.addEventListener("click", function (event) { if (event.target === detail) { detail.close(); } });
	$("detail-close").addEventListener("click", function () { detail.close(); });

	// --- live frames ----------------------------------------------------
	// One request's frames replay in order, STEP_MS apart, so each stage is
	// seen lighting up even when the real work took microseconds.
	function onFrame(frame) {
		const admission = {
			authenticated: frame.authenticated, principal: frame.principal, client: frame.client,
			operation: frame.operation, received_at_usec: frame.at_usec,
		};
		const item = record(frame.request_id, admission);
		// The first frames precede the credential check.
		if (frame.authenticated && !item.known) { identify(item, admission); }
		if (frame.phase === "skip" || frame.phase === "retry") { return; }
		item.queue.push(frame);
		pump(item);
	}
	function pump(item) {
		if (item.timer || !item.queue.length) { return; }
		const frame = item.queue[0];
		item.timer = setTimeout(function () {
			item.timer = null;
			item.queue.shift();
			play(item, frame);
			pump(item);
		}, frame.phase === "start" ? STEP_MS : 0);
	}
	function play(item, frame) {
		const key = item.op + ":" + frame.stage;
		if (frame.stage === "receive" && frame.phase === "start") {
			item.live = true;
			inFlight++;
			paintBusy();
		}
		if (frame.stage === "admission" && frame.phase === "finish" && item.known) {
			item.counted = true;
			touchAgent(item).live++;
			paintAgents();
		}
		if (frame.phase === "start") { stageStart(key); return; }
		if (frame.phase === "terminal") { finish(item, frame.state, frame.at_usec - item.at); return; }
		stageEnd(key, frame);
		if (frame.duration_usec && NAMES[frame.stage]) { item.stages.push([frame.stage, frame.duration_usec]); }
		if (frame.stage === "response" && item.live) {
			item.live = false;
			inFlight = Math.max(0, inFlight - 1);
			paintBusy();
			if (item.counted) {
				const agent = touchAgent(item);
				agent.live = Math.max(0, agent.live - 1);
				paintAgents();
			}
			repaint(item);
		}
	}

	async function loadRecent() {
		try {
			const response = await fetch("/v1/requests?tail=1&limit=" + KEEP, { headers: headers(), cache: "no-store" });
			if (!response.ok) { return; }
			(await response.json()).result.requests.forEach(function (entry) {
				const item = record(entry.admission.request_id, entry.admission);
				item.tr.classList.remove("new");
				finish(item, entry.outcome.state, entry.outcome.finished_at_usec - entry.admission.received_at_usec);
			});
		} catch (err) { /* the connection status already says the service is away */ }
	}
	function setLink(state, text) {
		link.className = "link " + state;
		linkText.textContent = text;
	}
	async function openStream() {
		clearTimeout(retry);
		if (stream) { stream.abort(); }
		const mine = stream = new AbortController();
		let response;
		try {
			response = await fetch("/v1/events", {
				headers: headers(cursor ? { Accept: "text/event-stream", "Last-Event-ID": cursor } : { Accept: "text/event-stream" }),
				signal: mine.signal, cache: "no-store",
			});
		} catch (err) {
			if (!mine.signal.aborted) { again(); }
			return;
		}
		if (response.status === 401 || response.status === 403) {
			setLink("down", bearer ? "Wrong token" : "Token needed");
			bearer = "";
			keyForm.hidden = false;
			return;
		}
		if (!response.ok) { again(); return; }
		keyForm.hidden = true;
		setLink("up", "Live");
		loadRecent();
		const reader = response.body.getReader(), decoder = new TextDecoder();
		let buffer = "";
		try {
			for (;;) {
				const chunk = await reader.read();
				if (chunk.done) { break; }
				buffer += decoder.decode(chunk.value, { stream: true });
				let cut;
				while ((cut = buffer.indexOf("\n\n")) >= 0) {
					onEvent(buffer.slice(0, cut));
					buffer = buffer.slice(cut + 2);
				}
			}
		} catch (err) { /* fall through to reconnect */ }
		if (!mine.signal.aborted) { again(); }
	}
	function onEvent(block) {
		let kind = "", id = "", data = "";
		block.split("\n").forEach(function (line) {
			if (line.startsWith("event: ")) { kind = line.slice(7); }
			else if (line.startsWith("id: ")) { id = line.slice(4); }
			else if (line.startsWith("data: ")) { data = line.slice(6); }
		});
		if (id) { cursor = id; }
		if (kind === "gap") {
			resetStages();
			agents.forEach(function (agent) { agent.live = 0; });
			loadRecent();
			return;
		}
		if (kind !== "stage" || !data) { return; }
		try { onFrame(JSON.parse(data)); } catch (err) { /* ignore a malformed frame */ }
	}
	function again() {
		setLink("down", "Reconnecting");
		resetStages();
		retry = setTimeout(openStream, 2000);
	}
	async function health() {
		try {
			const response = await fetch("/healthz", { cache: "no-store" });
			alertBox.hidden = response.ok;
			if (!response.ok) { alertBox.textContent = "Mousa stopped recording requests. Restart the service."; }
		} catch (err) { /* the connection status covers an unreachable service */ }
	}

	keyForm.addEventListener("submit", function (event) {
		event.preventDefault();
		bearer = keyInput.value.trim();
		keyInput.value = "";
		openStream();
	});
	setInterval(paintAgents, 30000);
	setInterval(health, 15000);
	health();
	openStream();
})();
