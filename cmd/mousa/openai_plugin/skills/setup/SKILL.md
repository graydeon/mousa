---
name: mousa-setup
description: Confirm the permitted local Mousa sources and data-sharing consent before using evidence.
---

# Connect Mousa

Explain before retrieving: the configured host and its model provider receive tool inputs/results, selected evidence text, source/item labels, canonical identifiers, byte coordinates, digests and provenance/audit metadata. Mousa stores canonical evidence and authorization/Source Trail audit records locally. Mousa does not filter secrets. Only connect sources the user intends to share. Host retention and account policy are controlled by the host/provider, not Mousa.

Use the user's existing authorization for the configured source labels and data sharing. Ask for confirmation only when that authorization is missing or the requested source scope changes. If consent is absent, stop without calling tools. A previous installation is not permission to broaden sources, enable ingestion, change caller identity or access arbitrary files.

Use mousa_status for each approved configured source to check collection/recovery state. Do not claim evidence is available merely because a tool is discovered. If a source is empty, unavailable, denied or withdrawn, explain the state and ask the user to handle ingestion or policy through their local Mousa setup. Do not silently enable writes or remove a deny.

On supported desktop hosts, explain that composer mentions search Mousa evidence and attach resource links. Resource reads reauthorize access; old links may become unavailable after policy or source changes. On CLI or hosts without mentions, use mousa_query and mousa_trail through the mousa-evidence skill.

Do not create a tunnel, deploy an endpoint, install credentials, publish a plugin, run a model/provider test or change host configuration without explicit authorization. The local generated package is not a public directory submission. Connection and actual host extension rendering must be checked on the host, not inferred from this skill.

The bundled skills guide client setup and evidence use. They do not grant administration access or turn internal development contracts into MCP capabilities. Check the configured tool schema and current capability documentation before promising an interface. Native declaration and activation commands are separate from these client tools; current queries do not enforce supersession.
