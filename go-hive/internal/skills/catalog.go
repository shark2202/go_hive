package skills

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const CompactThresholdChars = 5000

const DescriptionCapChars = 300

const mandatoryHeaderFull = `## Skills (mandatory)
Before replying: scan <available_skills> <description> entries.
- If the user message contains <read_skill>NAME</read_skill> tags: these are
  explicit requests. Read EVERY tagged skill's SKILL.md with
  ` + "`terminal_exec(\"cat <location>\")`" + ` before planning or other tool use — the
  tags override the "clearly applies" test and the one-skill limit below.
- Otherwise, if exactly one skill clearly applies: read its SKILL.md at <location> with ` + "`terminal_exec(\"cat <location>\")`" + `, then follow it.
- If multiple could apply: choose the most specific one, then read/follow it.
- If none clearly apply: do not read any SKILL.md.
- The read prevails over ask pressure: reading SKILL.md is pre-flight, not
  real work. Do it BEFORE any clarifying question (ask_user) and before any
  task plan — never defer it behind either.
Constraints: never read more than one skill up front unless <read_skill> tags name more; only read after selecting.
- When a skill drives external API writes (Gmail, Calendar, GitHub, etc.),
  assume rate limits: prefer fewer larger writes, avoid tight one-item loops,
  serialize bursts when possible, and respect 429/Retry-After.
- When a selected skill references a relative path, resolve it against the
  skill directory (parent of SKILL.md) and use that absolute path in tool commands.`

const mandatoryHeaderCompact = `## Skills (mandatory)
Before replying: scan <available_skills> <name> entries.
- If the user message contains <read_skill>NAME</read_skill> tags: these are
  explicit requests. Read EVERY tagged skill's SKILL.md with
  ` + "`terminal_exec(\"cat <location>\")`" + ` before planning or other tool use — the
  tags override the "clearly applies" test and the one-skill limit below.
- Otherwise, if exactly one skill clearly applies: read its SKILL.md at <location> with ` + "`terminal_exec(\"cat <location>\")`" + `, then follow it.
- If multiple could apply: choose the most specific one, then read/follow it.
- If none clearly apply: do not read any SKILL.md.
- The read prevails over ask pressure: reading SKILL.md is pre-flight, not
  real work. Do it BEFORE any clarifying question (ask_user) and before any
  task plan — never defer it behind either.
Constraints: never read more than one skill up front unless <read_skill> tags name more; only read after selecting.
- When a skill drives external API writes (Gmail, Calendar, GitHub, etc.),
  assume rate limits: prefer fewer larger writes, avoid tight one-item loops,
  serialize bursts when possible, and respect 429/Retry-After.
- When a selected skill references a relative path, resolve it against the
  skill directory (parent of SKILL.md) and use that absolute path in tool commands.`

type SkillCatalog struct {
	skills map[string]*ParsedSkill
	order  []string
}

func NewSkillCatalog(skills []*ParsedSkill) *SkillCatalog {
	c := &SkillCatalog{skills: map[string]*ParsedSkill{}}
	for _, skill := range skills {
		c.Add(skill)
	}
	return c
}

func (c *SkillCatalog) Add(skill *ParsedSkill) {
	if c.skills == nil {
		c.skills = map[string]*ParsedSkill{}
	}
	if _, exists := c.skills[skill.Name]; !exists {
		c.order = append(c.order, skill.Name)
	}
	c.skills[skill.Name] = skill
}

func (c *SkillCatalog) Get(name string) *ParsedSkill {
	return c.skills[name]
}

func (c *SkillCatalog) SkillCount() int { return len(c.skills) }

func (c *SkillCatalog) AllowlistedDirs() []string {
	out := make([]string, 0, len(c.order))
	for _, name := range c.order {
		out = append(out, c.skills[name].BaseDir)
	}
	return out
}

func (c *SkillCatalog) ToPrompt(phase *string) string {
	skills := make([]*ParsedSkill, 0, len(c.skills))
	for _, skill := range c.skills {
		if phase != nil && skill.Visibility != nil && !containsString(skill.Visibility, *phase) {
			continue
		}
		skills = append(skills, skill)
	}
	if len(skills) == 0 {
		return ""
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })

	fullXML := renderSkillXML(skills, false)
	if utf8.RuneCountInString(fullXML) <= CompactThresholdChars {
		return mandatoryHeaderFull + "\n\n" + fullXML
	}
	return mandatoryHeaderCompact + "\n\n" + renderSkillXML(skills, true)
}

func containsString(items []string, needle string) bool {
	for _, item := range items {
		if item == needle {
			return true
		}
	}
	return false
}

func renderSkillXML(skills []*ParsedSkill, compact bool) string {
	var b strings.Builder
	b.WriteString("<available_skills>")
	for _, skill := range skills {
		b.WriteString("\n  <skill>")
		b.WriteString("\n    <name>")
		b.WriteString(xmlEscape(skill.Name))
		b.WriteString("</name>")
		if !compact {
			b.WriteString("\n    <description>")
			b.WriteString(xmlEscape(CapDescription(skill.Description)))
			b.WriteString("</description>")
		}
		b.WriteString("\n    <location>")
		b.WriteString(xmlEscape(skill.Location))
		b.WriteString("</location>")
		b.WriteString("\n  </skill>")
	}
	b.WriteString("\n</available_skills>")
	return b.String()
}

func CapDescription(description string) string {
	text := strings.TrimSpace(description)
	if text == "" {
		return text
	}
	runes := []rune(text)
	for i, ch := range runes {
		if ch == '.' || ch == '!' || ch == '?' {
			if i+1 == len(runes) || unicode.IsSpace(runes[i+1]) {
				sentence := string(runes[:i+1])
				if len([]rune(sentence)) <= DescriptionCapChars {
					return sentence
				}
				break
			}
		}
	}
	if len(runes) <= DescriptionCapChars {
		return text
	}
	return strings.TrimRight(string(runes[:DescriptionCapChars-1]), " \t\n\r\f\v") + "…"
}

func xmlEscape(s string) string {
	if !strings.ContainsAny(s, "&<>") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
