package queen

// Parity: framework/agents/queen/queen_profiles.py::format_queen_identity_prompt

import (
	"fmt"
	"strings"
)

func identityString(value any) string {
	if value == nil {
		return "None"
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func cleanText(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func sentence(value any) string {
	text := cleanText(value)
	if text == "" {
		return ""
	}
	if strings.HasSuffix(text, ".") || strings.HasSuffix(text, "!") || strings.HasSuffix(text, "?") {
		return text
	}
	return text + "."
}

var identityReplacements = [][2]string{
	{"She thrives", "You thrive"},
	{"she thrives", "you thrive"},
	{"She's ", "You are "},
	{"she's ", "you are "},
	{"She is ", "You are "},
	{"she is ", "you are "},
	{"She ", "You "},
	{"she ", "you "},
	{"Her ", "Your "},
	{"her ", "your "},
}

func profileTextToInstruction(value any) string {
	instruction := sentence(value)
	for _, replacement := range identityReplacements {
		instruction = strings.ReplaceAll(instruction, replacement[0], replacement[1])
	}
	return instruction
}

func asMap(value any) map[string]any {
	m, _ := value.(map[string]any)
	return m
}

func asList(value any) []any {
	list, _ := value.([]any)
	return list
}

func mapText(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	text, _ := m[key].(string)
	return text
}

func mapAny(m map[string]any, key string) any {
	if m == nil {
		return nil
	}
	return m[key]
}

func FormatQueenIdentityPrompt(profile map[string]any, maxExamples *int) string {
	name, ok := profile["name"]
	if !ok {
		name = "the Queen"
	}
	title, ok := profile["title"]
	if !ok {
		title = "Senior Advisor"
	}
	core := profile["core_traits"]
	bg := asMap(profile["hidden_background"])
	psych := asMap(profile["psychological_profile"])
	triggers := asList(profile["behavior_triggers"])
	lore := asMap(profile["world_lore"])
	skills := mapText(profile, "skills")

	sections := make([]string, 0, 10)

	sections = append(sections, "<core_identity>\nYou are a queen agent, your name is "+
		identityString(name)+", "+identityString(title)+".\n"+identityString(core)+"\n</core_identity>")

	if len(bg) > 0 {
		sections = append(sections, "<hidden_background>\n"+
			"(Strictly hidden from users -- acts as your underlying "+
			"behavioral engine)\n"+
			"- Past Wound: "+mapText(bg, "past_wound")+"\n"+
			"- Deep Motive: "+mapText(bg, "deep_motive")+"\n"+
			"- Behavioral Mapping: "+mapText(bg, "behavioral_mapping")+"\n"+
			"</hidden_background>")
	}

	if len(psych) > 0 {
		psychLines := make([]string, 0, 2)
		if cleanText(mapAny(psych, "social_masks")) != "" {
			psychLines = append(psychLines, "- Social Masks & Boundaries: "+mapText(psych, "social_masks"))
		}
		antiStereotype := profileTextToInstruction(mapAny(psych, "anti_stereotype"))
		if antiStereotype != "" {
			psychLines = append(psychLines, "- Anti-Stereotype Rules: "+antiStereotype)
		}
		if len(psychLines) > 0 {
			sections = append(sections, "<psychological_profile>\n"+strings.Join(psychLines, "\n")+"\n</psychological_profile>")
		}
	}

	triggerLines := make([]string, 0, len(triggers))
	for _, raw := range triggers {
		trigger := asMap(raw)
		triggerLines = append(triggerLines, "  - ["+mapText(trigger, "trigger")+"]: "+profileTextToInstruction(mapAny(trigger, "reaction")))
	}
	sections = append(sections, "<behavior_rules>\n"+
		"- Before each response, internally assess:\n"+
		"  1. Relationship with the person (founder, engineer, "+
		"executive, stranger)\n"+
		"  2. Current context (urgency, stakes, emotional state)\n"+
		"  3. Filter through your hidden background and motives\n"+
		"  4. Select the right register and depth\n"+
		"- Interaction triggers:\n"+strings.Join(triggerLines, "\n")+"\n"+
		"</behavior_rules>")

	sections = append(sections, "<negative_constraints>\n"+
		"- NEVER use corporate filler ('leverage', 'synergy', "+
		"'circle back', 'at the end of the day').\n"+
		"- NEVER break character to explain your thought process "+
		"or reference your hidden background.\n"+
		"</negative_constraints>")

	if len(lore) > 0 {
		sections = append(sections, "<world_lore>\n- Habitat: "+mapText(lore, "habitat")+"\n- Lexicon: "+mapText(lore, "lexicon")+"\n</world_lore>")
	}

	if skills != "" {
		sections = append(sections, "<core_skills>\n"+skills+"\n</core_skills>")
	}

	examples := asList(profile["examples"])
	if maxExamples != nil {
		if *maxExamples < len(examples) {
			examples = examples[:*maxExamples]
		}
	}
	if len(examples) > 0 {
		parts := make([]string, 0, len(examples))
		for _, raw := range examples {
			example := asMap(raw)
			parts = append(parts, "User: "+mapText(example, "user")+"\n\nAssistant:\n"+mapText(example, "internal")+"\n"+mapText(example, "response"))
		}
		sections = append(sections, "<roleplay_examples>\n"+strings.Join(parts, "\n\n---\n\n")+"\n</roleplay_examples>")
	}

	return strings.Join(sections, "\n\n")
}
