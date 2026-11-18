package skills

import (
	"fmt"
	"log"
)

type SkillErrorCode string

const (
	SkillNotFound           SkillErrorCode = "SKILL_NOT_FOUND"
	SkillParseError         SkillErrorCode = "SKILL_PARSE_ERROR"
	SkillActivationFailed   SkillErrorCode = "SKILL_ACTIVATION_FAILED"
	SkillMissingDescription SkillErrorCode = "SKILL_MISSING_DESCRIPTION"
	SkillYAMLFixup          SkillErrorCode = "SKILL_YAML_FIXUP"
	SkillNameMismatch       SkillErrorCode = "SKILL_NAME_MISMATCH"
	SkillCollision          SkillErrorCode = "SKILL_COLLISION"
)

type SkillError struct {
	Code SkillErrorCode
	What string
	Why  string
	Fix  string
}

func NewSkillError(code SkillErrorCode, what, why, fix string) *SkillError {
	return &SkillError{Code: code, What: what, Why: why, Fix: fix}
}

func (e *SkillError) Error() string {
	return fmt.Sprintf("[%s]\nWhat failed: %s\nWhy: %s\nFix: %s", e.Code, e.What, e.Why, e.Fix)
}

func LogSkillError(level string, code SkillErrorCode, what, why, fix string) {
	log.Printf("[%s] What failed: %s | Why: %s | Fix: %s", code, what, why, fix)
}
