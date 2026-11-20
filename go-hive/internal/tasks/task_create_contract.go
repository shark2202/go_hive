package tasks

// Model-visible contract data for task_create's pivot arguments, byte-exact
// from the Python runtime's frozen tool contract
// (go_hive/contracts/tool-definitions.json, source:
// framework/tasks/tools/session_tools.py::_create_schema). The recheck lane
// compares these strings byte-for-byte, so they are data, not prose to edit
// locally.

const (
	// NewSessionFieldDescription documents task_create's new_session argument.
	NewSessionFieldDescription = "Whether to start this plan in a fresh session. Default false. This is the DM-phase pivot field; in COLONY phase the equivalent is `new_colony` (different field, same criteria — see `<pivot>` in the queen prompt for the universal contract).\n" +
		"\n" +
		"Set true ONLY for a clean pivot: the user introduces big new work that shares no goal, files, or topic with the current task list — typically the prior task is finished and they ask for something unrelated.\n" +
		"\n" +
		"Examples of the boundary:\n" +
		"- Just finished pulling a news digest; user says 'now start a CTO sourcing campaign.' -> new session (true): zero overlap with the digest.\n" +
		"- Running a CTO sourcing campaign; user says 'tweak the scoring rubric.' -> same session (false): it refines the work already underway.\n" +
		"\n" +
		"When true: a fresh session is created carrying ONLY your `handoff` note and this `tasks` plan — the old conversation is left behind, not copied or summarized. The user is silently swapped into it. Set this on your FIRST tool call of the turn, then end the turn — the new session's queen continues the work from there.\n" +
		"\n" +
		"Keep it false for follow-ups, clarifications, continuations, or anything that touches the current plan. When unsure, ask the user before forking."

	// HandoffDescription documents task_create's handoff argument.
	HandoffDescription = "Required when new_session=true. The new context inherits NOTHING from this conversation except what you write here — no transcript, no summary, no scrollback. So write a COMPLETE, self-contained handoff: everything the new context needs to know to carry the task plan through to done.\n" +
		"\n" +
		"Be descriptive and objective — state facts, not impressions. Cover, as applicable:\n" +
		"- The user's actual goal and intent for this work, in their own terms.\n" +
		"- Concrete facts and data established so far: names, URLs, IDs, file paths, the account/alias to use, numbers, exact requirements and wording.\n" +
		"- Decisions already made, and options already considered and ruled out (with the reason).\n" +
		"- Constraints, preferences, deadlines, and what 'done' looks like.\n" +
		"- Anything already attempted or partially completed that the new context must not redo.\n" +
		"\n" +
		"The new context cannot look back — if a detail matters for finishing the task and is not already in a task subject/description, it MUST be in this handoff. Write it in full now, while you still have the whole conversation in view. Ignored when new_session is false."
)
