"""Verify the lab's full report and copy each member into an Orrery artifact."""
import json
import sys
from pathlib import Path

source, output = map(Path, sys.argv[1:])
report = json.loads(source.read_bytes())
profiles = {
    ".github": set(),
    "orrery": {
        (engine, fact)
        for engine in ("rust", "ts-js")
        for fact in ("dependencies", "architecture", "files", "call-graph")
    },
    "scryer": {("go", fact) for fact in ("dependencies", "architecture", "files", "call-graph")},
    "scryer-go-engine": {("go", "call-graph")},
    "scryer-ts-js-engine": {("go", "call-graph")},
    "scryer-rust-engine": {("go", "call-graph")},
    "scryer-cpp-engine": {("go", "call-graph")},
    "scryer-java-engine": {("go", "call-graph")},
}
members = {member["path"]: member for member in report["projects"]}
assert set(members) == set(profiles), members.keys()
index = []
for path, expected in profiles.items():
    member = members[path]
    assert not member.get("error"), (path, member.get("error"))
    facts = member["facts"]
    assert facts["loc"]["total"]["files"] > 0, path
    outcomes = facts["engineFacts"]
    collected = {(x.get("engine"), x["fact"]): x["data"] for x in outcomes if x["status"] == "collected"}
    assert expected <= collected.keys(), (path, expected - collected.keys(), [(x.get("engine"), x["fact"], x["status"], x.get("message")) for x in outcomes])
    assert all(x["status"] != "failed" for x in outcomes), (path, outcomes)
    for (engine, fact), graph in collected.items():
        if fact != "call-graph":
            continue
        symbols = graph["symbols"]
        calls = graph["calls"]
        if engine == "go":
            ids = {symbol["symbolId"] for symbol in symbols}
            assert len(ids) == len(symbols) and ids and calls, path
            for call in calls:
                if target := call.get("targetSymbolId"):
                    assert target in ids and call["resolution"] == "resolved", call
                else:
                    assert call.get("resolutionReason"), call
                assert not call.get("callerSymbolId") or call["callerSymbolId"] in ids, call
    filename = ("github" if path == ".github" else path) + ".json"
    raw = json.dumps(facts, ensure_ascii=False, separators=(",", ":")).encode() + b"\n"
    assert len(raw) <= 20 * 1024 * 1024, (path, len(raw))
    (output / filename).write_bytes(raw)
    index.append({"path": path, "artifact": filename, "bytes": len(raw), "collected": sorted(collected)})

java_project = members["scryer-java-engine"]["facts"]
assert any(language["language"] == "Kotlin" for language in java_project["stack"]["languages"])
go = next(x["data"] for x in members["scryer"]["facts"]["engineFacts"] if x.get("engine") == "go" and x["fact"] == "call-graph")
assert any(c["resolution"] == "resolved" and c.get("targetSymbolId") for c in go["calls"])
assert any(c["site"]["file"] == "internal/engineabi/loader_cgo.go" and "C.engine_symbol" == c["callee"] and not c.get("targetSymbolId") for c in go["calls"])
if source.stat().st_size <= 20 * 1024 * 1024:
    (output / "workspace.json").write_bytes(source.read_bytes())
report["projects"] = [{"path": item["path"], "artifact": item["artifact"]} for item in index]
(output / "workspace-summary.json").write_text(json.dumps(report, ensure_ascii=False, separators=(",", ":")) + "\n")
(output / "index.json").write_text(json.dumps({
    "schemaVersion": 1, "wholeReportBytes": source.stat().st_size, "projects": index,
}, indent=2) + "\n")
print(f"Verified {len(index)} projects, full JSON {source.stat().st_size:,} bytes")
