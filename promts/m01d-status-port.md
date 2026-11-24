# Milestone 1d prompt: status shows the listener port

One-shot prompt. Do not re-run once done.

```text
Read AGENTS.md first.

Task: Milestone 1d only. `slb status` prints the listener line as `Listener: 127.0.0.1/tcp` without the port. Show the full listener (address:port/protocol, or address:first-last/protocol for a range) in both the text and the --json output. Check that the JSON fields are explicit (address, port or port_range, protocol). Add or update a test for the status formatting. Do not change anything else. Run the checks in AGENTS.md ("Definition of done") and report.
```
