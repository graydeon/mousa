// Mousa live view. Everything is rendered with textContent from metadata the
// service sends; no request or note text ever reaches this page.
(function () {
	"use strict";

	const STEP_MS = 140; // playback gap so a millisecond request is still visible
	const RECENT = 40;
	const SLOW_MS = 250;
	const VERBS = { query: "searched", note: "saved a note", document: "updated a document" };
	const ENDS = {
		committed: "done", no_op: "already saved", denied: "refused", invalid: "bad request",
		failed: "failed", canceled: "canceled", interrupted: "cut off",
	};

	const $ = function (id) { return document.getElementById(id); };
	const link = $("link"), linkText = $("link-text"), keyForm = $("key-form"), keyInput = $("key");
	const alertBox = $("alert"), agentList = $("agents"), recentList = $("recent"), quiet = $("quiet"), detail = $("detail");

	const nodes = {};   // stage -> { el, note, busy, times }
	document.querySelectorAll(".node").forEach(function (el) {
		nodes[el.dataset.stage] = { el: el, note: el.querySelector("i"), busy: 0, times: [] };
	});
	const requests = new Map(); // request id -> record
	const agents = new Map();   // agent name -> record
	let bearer = "", cursor = "", stream = null, retry = null;

	function el(tag, cls, text) {
		const node = document.createElement(tag);
		if (cls) { node.className = cls; }
		if (text !== undefined) { node.textContent = text; }
		return node;
	}
	function ms(usec) {
		const value = usec / 1000;
		if (value < 0.1) { return "<0.1 ms"; }
		return value >= 100 ? Math.round(value) + " ms" : value >= 10 ? value.toFixed(0) + " ms" : value.toFixed(1) + " ms";
	}
	function clock(usec) {
		return new Date(usec / 1000).toLocaleTimeString([], { hour12: false });
	}
	function ago(usec) {
		const seconds = Math.max(0, Math.round((Date.now() - usec / 1000) / 1000));
		if (seconds < 60) { return "just now"; }
		if (seconds < 3600) { return Math.round(seconds / 60) + " min ago"; }
		if (seconds < 86400) { return Math.round(seconds / 3600) + " h ago"; }
		return Math.round(seconds / 86400) + " d ago";
	}
	function headers(extra) {
		const out = extra || {};
		if (bearer) { out.Authorization = "Bearer " + bearer; }
		return out;
	}
	function setLink(state, text) {
		link.className = "link " + state;
		linkText.textContent = text;
	}

	// --- pipeline nodes -------------------------------------------------
	function nodeNote(node) {
		if (!node.times.length) { return; }
		const sorted = node.times.slice().sort(function (a, b) { return a - b; });
		node.note.textContent = ms(sorted[Math.floor(sorted.length / 2)]);
	}
	function stageStart(stage) {
		const node = nodes[stage];
		if (!node) { return; }
		node.busy++;
		node.el.classList.remove("bad");
		node.el.classList.add("on");
	}
	function stageEnd(stage, frame) {
		const node = nodes[stage];
		if (!node) { return; }
		node.busy = Math.max(0, node.busy - 1);
		if (node.busy === 0) { node.el.classList.remove("on", "slow"); }
		if (frame.phase === "error") {
			node.el.classList.add("bad");
			node.note.textContent = stage === "access" ? "refused" : "failed";
			return;
		}
		if (frame.duration_usec) {
			node.times.push(frame.duration_usec);
			if (node.times.length > 30) { node.times.shift(); }
			if (frame.duration_usec / 1000 > SLOW_MS) { node.el.classList.add("slow"); }
			nodeNote(node);
		}
	}
	function resetNodes() {
		Object.keys(nodes).forEach(function (stage) {
			nodes[stage].busy = 0;
			nodes[stage].el.classList.remove("on", "slow");
		});
	}

	// --- agents ---------------------------------------------------------
	function agentName(admission) {
		if (!admission.authenticated) { return "Unknown caller"; }
		return (admission.client && admission.client.harness) || admission.principal;
	}
	function touchAgent(record) {
		const name = record.who;
		let agent = agents.get(name);
		if (!agent) {
			agent = { name: name, live: 0, li: el("li"), title: el("b", "", name), model: el("small"), seen: el("small") };
			agent.li.append(agent.title, agent.model, agent.seen);
			agents.set(name, agent);
			agentList.appendChild(agent.li);
		}
		if (record.model) { agent.model.textContent = record.model; }
		agent.last = Math.max(agent.last || 0, record.at);
		return agent;
	}
	function paintAgents() {
		agents.forEach(function (agent) {
			agent.li.classList.toggle("on", agent.live > 0);
			agent.seen.textContent = agent.live > 0 ? "working now" : ago(agent.last);
		});
	}

	// --- recent list ----------------------------------------------------
	function record(id, admission) {
		let item = requests.get(id);
		if (item) { return item; }
		const client = admission.client || {};
		item = {
			id: id, op: admission.operation, who: agentName(admission), model: client.model || "",
			agentTag: client.agent || "", run: client.run_id || "", key: admission.principal || "",
			at: admission.received_at_usec || Date.now() * 1000, state: "", took: 0, stages: [], queue: [], timer: null, known: !!admission.authenticated,
		};
		item.li = el("li", "new");
		item.button = el("button");
		item.button.type = "button";
		item.time = el("time", "", clock(item.at));
		item.line = el("span", "who");
		item.line.append(el("b", "", item.who), document.createTextNode(" " + (VERBS[item.op] || item.op)));
		item.tookEl = el("span", "took");
		item.end = el("span", "end wait", "working");
		item.button.append(item.time, item.line, item.tookEl, item.end);
		item.button.addEventListener("click", function () { showDetail(item); });
		item.li.appendChild(item.button);
		requests.set(id, item);
		recentList.insertBefore(item.li, recentList.firstChild);
		quiet.hidden = true;
		while (recentList.children.length > RECENT) {
			const last = recentList.lastChild;
			requests.forEach(function (other, key) { if (other.li === last) { requests.delete(key); } });
			last.remove();
		}
		return item;
	}
	function finish(item, state, tookUsec) {
		touchAgent(item);
		item.state = state;
		if (tookUsec) { item.took = tookUsec; item.tookEl.textContent = ms(tookUsec); }
		const good = state === "committed" || state === "no_op";
		item.end.className = "end" + (good ? "" : " bad");
		item.end.textContent = ENDS[state] || state;
		paintAgents();
	}

	// --- live frames ----------------------------------------------------
	// Frames for one request are replayed STEP_MS apart, in order, so each
	// stage is seen lighting up even when the real work took microseconds.
	function onFrame(frame) {
		const admission = {
			authenticated: frame.authenticated, principal: frame.principal, client: frame.client,
			operation: frame.operation, received_at_usec: frame.at_usec,
		};
		const item = record(frame.request_id, admission);
		if (frame.authenticated && !item.known) {
			// The first frames precede the credential check; name the caller
			// once the service has.
			const client = frame.client || {};
			item.known = true;
			item.who = agentName(admission);
			item.model = client.model || "";
			item.agentTag = client.agent || "";
			item.run = client.run_id || "";
			item.key = frame.principal || "";
			item.line.firstChild.textContent = item.who;
		}
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
		if (frame.stage === "admission" && frame.phase === "finish") {
			item.live = true;
			touchAgent(item).live++;
			paintAgents();
		}
		if (frame.phase === "start") { stageStart(frame.stage); return; }
		if (frame.phase === "terminal") {
			finish(item, frame.state, frame.at_usec - item.at);
			return;
		}
		stageEnd(frame.stage, frame);
		if (frame.duration_usec) { item.stages.push([frame.stage, frame.duration_usec]); }
		if (frame.stage === "response" && item.live) {
			item.live = false;
			const agent = touchAgent(item);
			agent.live = Math.max(0, agent.live - 1);
			paintAgents();
		}
	}
	function onGap() {
		resetNodes();
		agents.forEach(function (agent) { agent.live = 0; });
		loadRecent();
	}

	async function loadRecent() {
		try {
			const response = await fetch("/v1/requests?tail=1&limit=" + RECENT, { headers: headers(), cache: "no-store" });
			if (!response.ok) { return; }
			const page = (await response.json()).result;
			page.requests.forEach(function (entry) {
				const item = record(entry.admission.request_id, entry.admission);
				item.li.classList.remove("new");
				finish(item, entry.outcome.state, entry.outcome.finished_at_usec - entry.admission.received_at_usec);
			});
		} catch (err) { /* the stream status already says the service is away */ }
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
		const reader = response.body.getReader();
		const decoder = new TextDecoder();
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
		if (kind === "gap") { onGap(); return; }
		if (kind !== "stage" || !data) { return; }
		try { onFrame(JSON.parse(data)); } catch (err) { /* ignore a malformed frame */ }
	}
	function again() {
		setLink("down", "Reconnecting");
		resetNodes();
		retry = setTimeout(openStream, 2000);
	}

	// --- detail ---------------------------------------------------------
	function row(list, name, value) {
		if (!value) { return; }
		list.append(el("dt", "", name), el("dd", "", value));
	}
	function showDetail(item) {
		detail.textContent = "";
		const close = el("button", "close", "Close");
		close.type = "button";
		close.addEventListener("click", function () { detail.hidden = true; });
		const list = el("dl");
		detail.append(close, el("h2", "", item.who + " " + (VERBS[item.op] || item.op)), list);
		row(list, "When", new Date(item.at / 1000).toLocaleString());
		row(list, "Result", ENDS[item.state] || "working");
		row(list, "Took", item.took ? ms(item.took) : "");
		row(list, "Model", item.model);
		row(list, "Task", item.agentTag);
		row(list, "Run", item.run);
		row(list, "Key", item.key);
		item.stages.forEach(function (stage) {
			const node = nodes[stage[0]];
			if (node) { row(list, node.el.querySelector("b").textContent, ms(stage[1])); }
		});
		detail.hidden = false;
		fetch("/v1/requests/" + encodeURIComponent(item.id), { headers: headers(), cache: "no-store" })
			.then(function (response) { return response.ok ? response.json() : null; })
			.then(function (envelope) {
				const evidence = envelope && envelope.result && envelope.result.evidence;
				if (!evidence || detail.hidden) { return; }
				row(list, "Source", evidence.source);
				row(list, "Item", evidence.item_id);
				row(list, "Record", evidence.kind.replace("_", " ") + " " + evidence.id.slice(0, 12));
			})
			.catch(function () { /* the summary above stands on its own */ });
	}

	// --- service health -------------------------------------------------
	async function health() {
		try {
			const response = await fetch("/healthz", { cache: "no-store" });
			alertBox.hidden = response.ok;
			if (!response.ok) { alertBox.textContent = "Mousa stopped recording requests. Restart the service."; }
		} catch (err) { /* the stream status covers an unreachable service */ }
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
