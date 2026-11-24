"use strict";

(function () {
	const ROW_LIMIT = 120;
	const STAGE_LIMIT = 32;
	const HISTORY_LIMIT = 64;
	const HISTORY_PAGES = 8;

	const tokenInput = document.getElementById("token");
	const attachButton = document.getElementById("attach-button");
	const connectionStatus = document.getElementById("connection-status");
	const gapBanner = document.getElementById("gap-banner");
	const requestList = document.getElementById("request-list");
	const emptyNote = document.getElementById("empty-note");
	const detailPanel = document.getElementById("request-detail");
	const filterHarness = document.getElementById("filter-harness");
	const filterOperation = document.getElementById("filter-operation");
	const filterOutcome = document.getElementById("filter-outcome");

	// The operator token lives only in this closure; it is never stored,
	// logged, or placed in a URL.
	let bearer = "";
	let controller = null;
	let eventCursor = "";
	let historyCursor = 0;
	const rows = new Map();
	const order = [];
	const harnesses = new Set();
	let reconnectTimer = 0;
	let detailController = null;
	let backfilling = false;
	let backfillPending = false;

	function el(tag, className, text) {
		const node = document.createElement(tag);
		if (className) {
			node.className = className;
		}
		if (text !== undefined && text !== null) {
			node.textContent = text;
		}
		return node;
	}

	function setStatus(text, live) {
		connectionStatus.textContent = text;
		connectionStatus.dataset.live = live ? "true" : "false";
	}

	function mapState(state) {
		switch (state) {
		case "committed": return "committed";
		case "no_op": return "no op";
		case "denied": return "denied";
		case "": return "in flight";
		default: return "failed";
		}
	}

	function reported(value) {
		return value && value !== "" ? { value: value, tag: "reported" } : { value: "unknown", tag: "unknown" };
	}

	function emptyModel(requestID) {
		return {
			requestID: requestID,
			operation: "",
			authenticated: false,
			principal: "",
			client: { harness: "", agent: "", model: "", runID: "" },
			receivedAtUsec: 0,
			stages: [],
			status: "in flight",
			seq: 0,
			errorCode: "",
		};
	}

	function modelFor(requestID) {
		let model = rows.get(requestID);
		if (!model) {
			model = emptyModel(requestID);
			rows.set(requestID, model);
			order.push(requestID);
			while (order.length > ROW_LIMIT) {
				const removed = order.shift();
				const old = rows.get(removed);
				if (old && old.node && old.node.parentNode) {
					old.node.parentNode.remove();
				}
				rows.delete(removed);
			}
		}
		return model;
	}

	function applyIdentity(model, source) {
		if (source.operation) {
			model.operation = source.operation;
		}
		if (source.authenticated !== undefined) {
			model.authenticated = !!source.authenticated;
		}
		if (source.principal) {
			model.principal = source.principal;
		}
		const client = source.client || {};
		if (client.harness !== undefined && client.harness !== "") {
			model.client.harness = client.harness;
		}
		if (client.agent !== undefined && client.agent !== "") {
			model.client.agent = client.agent;
		}
		if (client.model !== undefined && client.model !== "") {
			model.client.model = client.model;
		}
		if (client.run_id !== undefined && client.run_id !== "") {
			model.client.runID = client.run_id;
		}
		if (source.received_at_usec && !model.receivedAtUsec) {
			model.receivedAtUsec = source.received_at_usec;
		}
	}

	function matchesFilters(model) {
		const harness = filterHarness.value;
		if (harness && model.client.harness !== harness) {
			return false;
		}
		const operation = filterOperation.value;
		if (operation && model.operation !== operation) {
			return false;
		}
		const outcome = filterOutcome.value;
		if (outcome && model.status !== outcome) {
			return false;
		}
		return true;
	}

	function syncHarnessFilter() {
		for (const model of rows.values()) {
			if (model.client.harness) {
				harnesses.add(model.client.harness);
			}
		}
		const current = filterHarness.value;
		while (filterHarness.options.length > 1) {
			filterHarness.remove(1);
		}
		const sorted = Array.from(harnesses).sort();
		for (const name of sorted) {
			const option = document.createElement("option");
			option.value = name;
			option.textContent = name;
			filterHarness.appendChild(option);
		}
		filterHarness.value = current;
	}

	function formatUsec(usec) {
		if (!usec) {
			return "";
		}
		const date = new Date(Math.floor(usec / 1000));
		return date.toISOString().replace("T", " ").replace("Z", "Z");
	}

	function renderRow(model) {
		let node = model.node;
		let button;
		if (!node) {
			node = document.createElement("li");
			button = document.createElement("button");
			button.type = "button";
			button.className = "row-button";
			button.dataset.requestId = model.requestID;
			button.addEventListener("click", function () {
				openDetail(model.requestID, button);
			});
			node.appendChild(button);
			model.node = node;
			requestList.appendChild(node);
		}
		button = node.firstChild;
		button.textContent = "";
		button.setAttribute("aria-pressed", "false");

		const identity = el("span", "cell-id");
		const who = model.principal || "anonymous";
		identity.appendChild(el("span", "who", who + (model.authenticated ? " ·authenticated" : " ·unauthenticated")));
		const fields = [
			["harness", model.client.harness],
			["agent", model.client.agent],
			["model", model.client.model],
			["run", model.client.runID],
		];
		for (const pair of fields) {
			const meta = el("span", "meta");
			const seen = reported(pair[1]);
			meta.appendChild(document.createTextNode(pair[0] + " "));
			meta.appendChild(el("b", "", seen.value));
			meta.appendChild(document.createTextNode(" " + seen.tag));
			identity.appendChild(meta);
			identity.appendChild(document.createTextNode(" "));
		}

		const request = el("span", "cell-request");
		request.appendChild(el("code", "", model.requestID));
		request.appendChild(el("span", "op", model.operation || "request"));
		if (model.receivedAtUsec) {
			request.appendChild(document.createTextNode(" "));
			request.appendChild(el("span", "at", formatUsec(model.receivedAtUsec)));
		}

		const stages = el("span", "cell-stages");
		const list = document.createElement("ul");
		for (const stage of model.stages) {
			const item = document.createElement("li");
			let cls = "stage";
			if (stage.phase === "skip") {
				cls += " skip";
			} else if (stage.phase === "retry") {
				cls += " retry";
			}
			item.className = cls;
			const name = el("b", "", stage.stage);
			item.appendChild(name);
			let suffix = "";
			if (stage.phase === "finish" && stage.duration_usec) {
				suffix = " " + stage.duration_usec + "µs";
			} else if (stage.phase === "terminal" && stage.state) {
				suffix = " " + stage.state;
			} else if (stage.phase !== "start") {
				suffix = " " + stage.phase;
			}
			item.appendChild(document.createTextNode(suffix));
			list.appendChild(item);
		}
		stages.appendChild(list);

		const record = el("span", "cell-record");
		const status = el("span", "status " + model.status.replace(" ", "-"), model.status);
		if (model.status === "in flight") {
			status.className = "status flight";
		}
		record.appendChild(status);
		if (model.seq) {
			record.appendChild(el("span", "seq", "seq " + model.seq));
		}
		if (model.errorCode) {
			record.appendChild(el("span", "seq", model.errorCode));
		}

		button.appendChild(identity);
		button.appendChild(request);
		button.appendChild(stages);
		button.appendChild(record);
		node.hidden = !matchesFilters(model);
		emptyNote.hidden = rows.size !== 0;
	}

	function refreshAll() {
		for (const model of rows.values()) {
			renderRow(model);
		}
		emptyNote.hidden = rows.size !== 0;
	}

	function handleStageFrame(frame, eventID) {
		if (eventID) {
			eventCursor = eventID;
		}
		if (!frame.request_id) {
			return;
		}
		const model = modelFor(frame.request_id);
		applyIdentity(model, frame);
		if (frame.stage === "outcome" && frame.phase === "terminal") {
			model.status = mapState(frame.state);
		} else {
			model.stages.push({ stage: frame.stage, phase: frame.phase, duration_usec: frame.duration_usec || 0, state: frame.state || "" });
			if (model.stages.length > STAGE_LIMIT) {
				model.stages.shift();
			}
		}
		if (model.client.harness) {
			harnesses.add(model.client.harness);
		}
		renderRow(model);
		syncHarnessFilter();
	}

	function applyHistoryEntry(entry) {
		const admission = entry.admission || {};
		const outcome = entry.outcome || {};
		const requestID = outcome.request_id || admission.request_id;
		if (!requestID) {
			return;
		}
		const model = modelFor(requestID);
		applyIdentity(model, admission);
		model.status = mapState(outcome.state);
		model.seq = outcome.seq || 0;
		model.errorCode = outcome.error_code || "";
		if (model.client.harness) {
			harnesses.add(model.client.harness);
		}
		renderRow(model);
	}

	function handleGap() {
		gapBanner.hidden = false;
		if (controller) {
			backfillHistory(controller);
		}
	}

	// Single flight per stream: a second request while a page is in flight is
	// folded into one more bounded batch; batches yield between them so live
	// frames keep flowing. The history cursor advances to next_after only
	// after a whole page has been applied in order.
	async function backfillHistory(mine) {
		if (backfilling) {
			backfillPending = true;
			return;
		}
		backfilling = true;
		try {
			for (;;) {
				backfillPending = false;
				let more = true;
				for (let page = 0; page < HISTORY_PAGES && more; page++) {
					const response = await fetch("/v1/requests?after=" + historyCursor + "&limit=" + HISTORY_LIMIT, {
						headers: { Authorization: "Bearer " + bearer },
						cache: "no-store",
						signal: mine.signal,
					});
					if (mine !== controller || !response.ok) {
						return;
					}
					const envelope = await response.json();
					if (mine !== controller) {
						return;
					}
					const result = envelope.result || envelope;
					const entries = result.requests || [];
					for (const entry of entries) {
						applyHistoryEntry(entry);
					}
					historyCursor = result.next_after || historyCursor;
					more = result.more === true;
				}
				syncHarnessFilter();
				if (!more && !backfillPending) {
					return;
				}
				await new Promise(function (resolve) {
					setTimeout(resolve, 0);
				});
				if (mine !== controller) {
					return;
				}
			}
		} catch (err) {
			// Aborted or failed page fetches end the batch; the durable
			// record is re-read on the next gap or reconnect.
		} finally {
			backfilling = false;
			if (backfillPending && controller) {
				const current = controller;
				setTimeout(function () {
					if (controller === current) {
						backfillHistory(current);
					}
				}, 0);
			}
		}
	}

	function dispatchBlock(block) {
		let id = "";
		let kind = "message";
		const data = [];
		for (const line of block.split("\n")) {
			if (line === "" || line.startsWith(":")) {
				continue;
			}
			const cut = line.indexOf(":");
			const field = cut < 0 ? line : line.slice(0, cut);
			let value = cut < 0 ? "" : line.slice(cut + 1);
			if (value.startsWith(" ")) {
				value = value.slice(1);
			}
			if (field === "id") {
				id = value;
			} else if (field === "event") {
				kind = value;
			} else if (field === "data") {
				data.push(value);
			}
		}
		const payload = data.join("\n");
		if (kind === "stage" && payload) {
			try {
				handleStageFrame(JSON.parse(payload), id);
			} catch (err) {
				// Malformed frames are dropped; the durable record stays authoritative.
			}
		} else if (kind === "gap") {
			if (id) {
				eventCursor = id;
			}
			handleGap();
		}
	}

	async function openStream() {
		controller = new AbortController();
		const mine = controller;
		setStatus("Connecting…", false);
		const headers = {
			Authorization: "Bearer " + bearer,
			Accept: "text/event-stream",
		};
		if (eventCursor) {
			headers["Last-Event-ID"] = eventCursor;
		}
		let response;
		try {
			response = await fetch("/v1/events", {
				method: "GET",
				headers: headers,
				signal: mine.signal,
				cache: "no-store",
			});
		} catch (err) {
			if (mine.signal.aborted) {
				return;
			}
			setStatus("Disconnected — stream unreachable", false);
			scheduleReconnect();
			return;
		}
		if (!response.ok) {
			setStatus("Disconnected — refused " + response.status, false);
			if (response.status === 401 || response.status === 403) {
				// Leave a clean attach state so a fresh credential can be
				// entered; a rejected credential is never retried or kept.
				controller = null;
				bearer = "";
				return;
			}
			scheduleReconnect();
			return;
		}
		setStatus("Connected — live stage stream", true);
		attachButton.textContent = "Detach";
		attachButton.setAttribute("aria-pressed", "true");
		// Seed the ledger from the durable record; live frames extend it.
		backfillHistory(mine);
		const reader = response.body.getReader();
		const decoder = new TextDecoder();
		let buffer = "";
		try {
			for (;;) {
				const chunk = await reader.read();
				if (chunk.done) {
					break;
				}
				buffer += decoder.decode(chunk.value, { stream: true });
				let cut;
				while ((cut = buffer.indexOf("\n\n")) >= 0) {
					const block = buffer.slice(0, cut);
					buffer = buffer.slice(cut + 2);
					if (block.trim() !== "") {
						dispatchBlock(block);
					}
				}
			}
		} catch (err) {
			// Fall through to reconnect handling below.
		}
		if (mine.signal.aborted) {
			return;
		}
		setStatus("Disconnected — stream ended", false);
		scheduleReconnect();
	}

	function scheduleReconnect() {
		if (bearer === "" || reconnectTimer) {
			return;
		}
		attachButton.textContent = "Attach";
		attachButton.setAttribute("aria-pressed", "false");
		reconnectTimer = setTimeout(function () {
			reconnectTimer = 0;
			openStream();
		}, 1500);
	}

	function disconnect() {
		if (reconnectTimer) {
			clearTimeout(reconnectTimer);
			reconnectTimer = 0;
		}
		if (controller) {
			controller.abort();
			controller = null;
		}
		if (detailController) {
			detailController.abort();
			detailController = null;
		}
		setStatus("Disconnected", false);
		attachButton.textContent = "Attach";
		attachButton.setAttribute("aria-pressed", "false");
	}

	attachButton.addEventListener("click", function () {
		if (controller || reconnectTimer) {
			bearer = "";
			disconnect();
			return;
		}
		bearer = tokenInput.value.trim();
		tokenInput.value = "";
		if (bearer === "") {
			setStatus("Disconnected — enter an operator token", false);
			return;
		}
		openStream();
	});

	function openDetail(requestID, button) {
		button.setAttribute("aria-pressed", "true");
		detailPanel.textContent = "";
		detailPanel.appendChild(el("p", "detail-empty", "Loading verified record…"));
		if (detailController) {
			detailController.abort();
		}
		const mine = detailController = new AbortController();
		fetch("/v1/requests/" + encodeURIComponent(requestID), {
			headers: { Authorization: "Bearer " + bearer },
			cache: "no-store",
			signal: mine.signal,
		}).then(function (response) {
			if (!response.ok) {
				throw new Error("status " + response.status);
			}
			return response.json();
		}).then(function (envelope) {
			if (mine.signal.aborted) {
				return;
			}
			const entry = (envelope.result || envelope);
			renderDetail(requestID, entry);
		}).catch(function () {
			if (mine.signal.aborted) {
				return;
			}
			detailPanel.textContent = "";
			detailPanel.appendChild(el("p", "detail-empty", "Verified record unavailable for this request."));
		});
	}

	function detailRow(list, label, build) {
		list.appendChild(el("dt", "", label));
		const value = document.createElement("dd");
		build(value);
		list.appendChild(value);
	}

	function renderDetail(requestID, entry) {
		detailPanel.textContent = "";
		detailPanel.appendChild(el("h2", "", "Verified record"));
		const list = document.createElement("dl");
		const admission = entry.admission || {};
		const outcome = entry.outcome || {};
		const evidence = entry.evidence;

		detailRow(list, "request", function (dd) {
			dd.appendChild(el("code", "", requestID));
		});
		detailRow(list, "operation", function (dd) {
			dd.textContent = admission.operation || "unknown";
		});
		detailRow(list, "identity", function (dd) {
			const who = admission.principal || "anonymous";
			dd.textContent = who + (admission.authenticated ? " ·authenticated" : " ·unauthenticated");
		});
		detailRow(list, "outcome", function (dd) {
			dd.textContent = mapState(outcome.state) +
				(outcome.seq ? " ·seq " + outcome.seq : "") +
				(outcome.error_code ? " ·" + outcome.error_code : "");
		});
		if (outcome.finished_at_usec) {
			detailRow(list, "finished", function (dd) {
				dd.textContent = formatUsec(outcome.finished_at_usec);
			});
		}
		if (evidence) {
			detailRow(list, "evidence", function (dd) {
				dd.textContent = evidence.kind || "";
				if (evidence.id) {
					dd.appendChild(document.createTextNode(" "));
					dd.appendChild(el("code", "", evidence.id));
				}
			});
			if (evidence.source_id) {
				detailRow(list, "source id", function (dd) {
					dd.appendChild(el("code", "", evidence.source_id));
				});
			}
			if (evidence.source) {
				detailRow(list, "source", function (dd) {
					dd.textContent = evidence.source;
				});
			}
			if (evidence.item_id) {
				detailRow(list, "item", function (dd) {
					dd.appendChild(el("code", "", evidence.item_id));
				});
			}
			if (evidence.current) {
				detailRow(list, "current", function (dd) {
					dd.appendChild(el("code", "", evidence.current));
				});
			}
			if (evidence.previous) {
				detailRow(list, "previous", function (dd) {
					dd.appendChild(el("code", "", evidence.previous));
				});
			}
			if (evidence.note_author) {
				detailRow(list, "note author", function (dd) {
					dd.textContent = evidence.note_author;
				});
			}
			if (evidence.cited_trails && evidence.cited_trails.length) {
				detailRow(list, "cited trails", function (dd) {
					for (const trail of evidence.cited_trails) {
						dd.appendChild(el("code", "", trail));
						dd.appendChild(document.createTextNode(" "));
					}
				});
			}
			if (evidence.sources && evidence.sources.length) {
				detailRow(list, "sources", function (dd) {
					for (const source of evidence.sources) {
						dd.appendChild(el("code", "", source));
						dd.appendChild(document.createTextNode(" "));
					}
				});
			}
		}
		const model = rows.get(requestID);
		if (model && model.stages.length) {
			detailRow(list, "measured stages", function (dd) {
				const seen = document.createElement("ul");
				for (const stage of model.stages) {
					const item = document.createElement("li");
					item.textContent = stage.stage + " " + stage.phase +
						(stage.duration_usec ? " " + stage.duration_usec + "µs" : "");
					seen.appendChild(item);
				}
				dd.appendChild(seen);
				dd.appendChild(el("p", "meta", "Stage timings are live observations, not part of the durable record."));
			});
		}
		detailPanel.appendChild(list);
	}

	filterHarness.addEventListener("change", refreshAll);
	filterOperation.addEventListener("change", refreshAll);
	filterOutcome.addEventListener("change", refreshAll);
	// Try without a credential first: a service started with --open-monitor
	// streams at once; otherwise the refusal leaves the token field in use.
	openStream();
})();
