package skills

// Model-visible contract data for write_skill, byte-exact from the Python
// runtime's frozen tool contract (go_hive/contracts/tool-definitions.json,
// source: framework/tools/queen_lifecycle_tools.py::write_skill_tool). The
// recheck lane compares these strings byte-for-byte, so edit only together
// with the reference and a fresh capture.
//
// The Go port's own scope notes used to live in the tool description; they now
// live in the executor's error messages and comments, because the description
// is contract data the model reads on both runtimes.
const (
	writeSkillDescription = "Write or replace a skill, or install one (yours, a downloaded one, an existing skill folder). This is the way to add a skill: don't write SKILL.md into a skills folder with the terminal or write_file, since a skill put there by hand isn't loaded until a new session and skips the user's approval.\n" +
		"\n" +
		"SCOPE (who sees it):\n" +
		"  - 'queen' (default outside a colony): your own skills. In your skills catalog from your next turn and in your colonies'; no other queen sees it. A skill the user made in your skills can't be replaced.\n" +
		"  - 'colony' (default inside a colony): THIS colony's workers only. Use it BEFORE fanning out parallel workers when the per-task protocol is the same across them (schema, output format, tool conventions, quality bar): write the protocol ONCE, then point each worker at it BY NAME in the task string instead of repeating the prose. Workers spawned AFTER this call pick it up; existing workers do not.\n" +
		"  - 'global': the shared library every queen and colony reads. Needs the user's approval: nothing is installed yet, the user is asked in this session's chat in the Hive dashboard, and you get a message when they approve or deny. You don't wait for it; carry on.\n" +
		"\n" +
		"TWO MODES:\n" +
		"  - Inline: pass skill_name + skill_description + skill_body (and optional skill_files). Authors a fresh skill from arguments.\n" +
		"  - Copy: pass source_path pointing at an existing skill root directory (the one containing SKILL.md). The tool reads SKILL.md plus every other file in that directory and writes them into the chosen scope verbatim. Optionally pass skill_name to rename on copy. The other inline params are REJECTED in this mode — use inline mode if you need to edit content.\n" +
		"\n" +
		"Replacing your own skill of the same name is fine — your latest content wins.\n" +
		"\n" +
		"Skill body should read like a self-contained operating procedure: what the agent is doing, the exact tools/schema to use, the output format, what 'done' looks like. Skip the per-task specifics — those go in the task string."

	// skill_name
	writeSkillSkillNameDescription = "Lowercase, hyphen-separated, ≤64 chars (e.g. 'competitor-research-protocol'). Required in inline mode; optional in source_path mode (renames the skill on copy — defaults to the source's frontmatter name)."

	// skill_description
	writeSkillSummaryDescription = "One-line summary of what this skill teaches and when to use it. Surfaced in the skills catalog. Required in inline mode; rejected in source_path mode (description comes from the source SKILL.md frontmatter)."

	// skill_body
	writeSkillSkillBodyDescription = "Markdown body of SKILL.md — the operational procedure the agent needs to run it unattended. Required in inline mode; rejected in source_path mode."

	// skill_files
	writeSkillSkillFilesDescription = "Optional supporting files (scripts, JSON refs, etc.). Each entry is {path, content} where path is relative to the skill root. Inline mode only; rejected in source_path mode (the tool walks the source dir and copies aux files automatically)."

	// source_path
	writeSkillSourcePathDescription = "Absolute or '~'-prefixed path to an existing skill's root directory (the one that contains SKILL.md). When set, the tool copies the entire skill folder — SKILL.md + every other UTF-8 file — into the chosen scope. Use this to install a skill you downloaded (e.g. git clone it into your workspace first), or to lift a framework default, shared or another colony's skill into your own skills or THIS colony to customize it."

	// scope
	writeSkillScopeDescription = "Who sees the skill: 'queen' (you, and your colonies), 'colony' (this colony's workers), or 'global' (every queen and colony, after the user approves). Defaults to 'colony' inside a colony, 'queen' outside one."
)
