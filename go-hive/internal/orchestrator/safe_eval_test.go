package orchestrator

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"testing"
	"time"
)

func mustEval(t *testing.T, expression string, context map[string]any, options ...EvalOption) any {
	t.Helper()
	value, err := SafeEval(expression, context, options...)
	if err != nil {
		t.Fatalf("SafeEval(%q) returned error: %v", expression, err)
	}
	return value
}

func assertPyEqual(t *testing.T, expression string, got, want any) {
	t.Helper()
	expected := normalizeValue(want)
	if !pyEqual(got, expected) {
		t.Fatalf("SafeEval(%q) = %#v (%T), want %#v (%T)", expression, got, got, expected, expected)
	}
}

func assertPyApprox(t *testing.T, expression string, got any, want float64) {
	t.Helper()
	number, ok := got.(float64)
	if !ok {
		t.Fatalf("SafeEval(%q) = %#v (%T), want float %v", expression, got, got, want)
	}
	if math.Abs(number-want) > 1e-9 {
		t.Fatalf("SafeEval(%q) = %v, want approx %v", expression, number, want)
	}
}

func assertBool(t *testing.T, expression string, got any, want bool) {
	t.Helper()
	value, ok := got.(bool)
	if !ok || value != want {
		t.Fatalf("SafeEval(%q) = %#v (%T), want %v", expression, got, got, want)
	}
}

func assertNil(t *testing.T, expression string, got any) {
	t.Helper()
	if got != nil {
		t.Fatalf("SafeEval(%q) = %#v (%T), want None", expression, got, got)
	}
}

func requireEvalError[T error](t *testing.T, expression string, context map[string]any, match string, options ...EvalOption) T {
	t.Helper()
	_, err := SafeEval(expression, context, options...)
	if err == nil {
		t.Fatalf("SafeEval(%q) succeeded, want %T", expression, *new(T))
	}
	var target T
	if !errors.As(err, &target) {
		t.Fatalf("SafeEval(%q) returned %T (%v), want %T", expression, err, err, target)
	}
	if match != "" && !strings.Contains(err.Error(), match) {
		t.Fatalf("SafeEval(%q) error = %q, want it to contain %q", expression, err.Error(), match)
	}
	return target
}

func sortedValues(t *testing.T, expression string, got any) []string {
	t.Helper()
	values, ok := got.([]any)
	if !ok {
		t.Fatalf("SafeEval(%q) = %#v (%T), want list", expression, got, got)
	}
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = fmt.Sprint(value)
	}
	sort.Strings(out)
	return out
}

func assertStrings(t *testing.T, got []string, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// Parity: core/tests/test_safe_eval.py::TestLiterals::test_integer, core/tests/test_safe_eval.py::TestLiterals::test_negative_integer, core/tests/test_safe_eval.py::TestLiterals::test_float, core/tests/test_safe_eval.py::TestLiterals::test_string, core/tests/test_safe_eval.py::TestLiterals::test_double_quoted_string, core/tests/test_safe_eval.py::TestLiterals::test_boolean_true, core/tests/test_safe_eval.py::TestLiterals::test_boolean_false, core/tests/test_safe_eval.py::TestLiterals::test_none
func TestSafeEvalLiterals(t *testing.T) {
	t.Run("integer", func(t *testing.T) {
		assertPyEqual(t, "42", mustEval(t, "42", nil), int64(42))
	})
	t.Run("negative_integer", func(t *testing.T) {
		assertPyEqual(t, "-1", mustEval(t, "-1", nil), int64(-1))
	})
	t.Run("float", func(t *testing.T) {
		assertPyApprox(t, "3.14", mustEval(t, "3.14", nil), 3.14)
	})
	t.Run("string", func(t *testing.T) {
		assertPyEqual(t, "'hello'", mustEval(t, "'hello'", nil), "hello")
	})
	t.Run("double_quoted_string", func(t *testing.T) {
		assertPyEqual(t, `"world"`, mustEval(t, `"world"`, nil), "world")
	})
	t.Run("boolean_true", func(t *testing.T) {
		assertBool(t, "True", mustEval(t, "True", nil), true)
	})
	t.Run("boolean_false", func(t *testing.T) {
		assertBool(t, "False", mustEval(t, "False", nil), false)
	})
	t.Run("none", func(t *testing.T) {
		assertNil(t, "None", mustEval(t, "None", nil))
	})
}

// Parity: core/tests/test_safe_eval.py::TestDataStructures::test_list, core/tests/test_safe_eval.py::TestDataStructures::test_empty_list, core/tests/test_safe_eval.py::TestDataStructures::test_nested_list, core/tests/test_safe_eval.py::TestDataStructures::test_tuple, core/tests/test_safe_eval.py::TestDataStructures::test_dict, core/tests/test_safe_eval.py::TestDataStructures::test_empty_dict
func TestSafeEvalDataStructures(t *testing.T) {
	t.Run("list", func(t *testing.T) {
		assertPyEqual(t, "[1, 2, 3]", mustEval(t, "[1, 2, 3]", nil), []any{1, 2, 3})
	})
	t.Run("empty_list", func(t *testing.T) {
		assertPyEqual(t, "[]", mustEval(t, "[]", nil), []any{})
	})
	t.Run("nested_list", func(t *testing.T) {
		assertPyEqual(t, "[[1, 2], [3, 4]]", mustEval(t, "[[1, 2], [3, 4]]", nil), []any{[]any{1, 2}, []any{3, 4}})
	})
	t.Run("tuple", func(t *testing.T) {
		assertPyEqual(t, "(1, 2, 3)", mustEval(t, "(1, 2, 3)", nil), Tuple{1, 2, 3})
	})
	t.Run("dict", func(t *testing.T) {
		assertPyEqual(t, "{'a': 1, 'b': 2}", mustEval(t, "{'a': 1, 'b': 2}", nil), map[string]any{"a": 1, "b": 2})
	})
	t.Run("empty_dict", func(t *testing.T) {
		assertPyEqual(t, "{}", mustEval(t, "{}", nil), map[string]any{})
	})
}

// Parity: core/tests/test_safe_eval.py::TestArithmetic::test_addition, core/tests/test_safe_eval.py::TestArithmetic::test_subtraction, core/tests/test_safe_eval.py::TestArithmetic::test_multiplication, core/tests/test_safe_eval.py::TestArithmetic::test_division, core/tests/test_safe_eval.py::TestArithmetic::test_floor_division, core/tests/test_safe_eval.py::TestArithmetic::test_modulo, core/tests/test_safe_eval.py::TestArithmetic::test_power, core/tests/test_safe_eval.py::TestArithmetic::test_power_large_exponent_blocked, core/tests/test_safe_eval.py::TestArithmetic::test_power_large_result_blocked, core/tests/test_safe_eval.py::TestArithmetic::test_nested_power_blocked, core/tests/test_safe_eval.py::TestArithmetic::test_complex_expression
func TestSafeEvalArithmetic(t *testing.T) {
	t.Run("addition", func(t *testing.T) {
		assertPyEqual(t, "2 + 3", mustEval(t, "2 + 3", nil), int64(5))
	})
	t.Run("subtraction", func(t *testing.T) {
		assertPyEqual(t, "10 - 4", mustEval(t, "10 - 4", nil), int64(6))
	})
	t.Run("multiplication", func(t *testing.T) {
		assertPyEqual(t, "3 * 7", mustEval(t, "3 * 7", nil), int64(21))
	})
	t.Run("division", func(t *testing.T) {
		assertPyApprox(t, "10 / 4", mustEval(t, "10 / 4", nil), 2.5)
	})
	t.Run("floor_division", func(t *testing.T) {
		assertPyEqual(t, "10 // 3", mustEval(t, "10 // 3", nil), int64(3))
	})
	t.Run("modulo", func(t *testing.T) {
		assertPyEqual(t, "10 % 3", mustEval(t, "10 % 3", nil), int64(1))
	})
	t.Run("power", func(t *testing.T) {
		assertPyEqual(t, "2 ** 10", mustEval(t, "2 ** 10", nil), int64(1024))
	})
	t.Run("power_large_exponent_blocked", func(t *testing.T) {
		requireEvalError[*ValueError](t, "2 ** 1001", nil, "Power exponent")
	})
	t.Run("power_large_result_blocked", func(t *testing.T) {
		requireEvalError[*ValueError](t, "99 ** 1000", nil, "Power operation")
	})
	t.Run("nested_power_blocked", func(t *testing.T) {
		requireEvalError[*ValueError](t, "2 ** 2 ** 20", nil, "Power exponent")
	})
	t.Run("complex_expression", func(t *testing.T) {
		assertPyEqual(t, "(2 + 3) * 4 - 1", mustEval(t, "(2 + 3) * 4 - 1", nil), int64(19))
	})
}

func fakeClock(times ...time.Time) func() time.Time {
	index := 0
	return func() time.Time {
		if index < len(times) {
			current := times[index]
			index++
			return current
		}
		return times[len(times)-1]
	}
}

// Parity: core/tests/test_safe_eval.py::TestExecutionTimeout::test_default_timeout, core/tests/test_safe_eval.py::TestExecutionTimeout::test_timeout_must_be_positive, core/tests/test_safe_eval.py::TestExecutionTimeout::test_timeout_can_be_disabled, core/tests/test_safe_eval.py::TestExecutionTimeout::test_timeout_exceeded_raises
func TestSafeEvalExecutionTimeout(t *testing.T) {
	t.Run("default_timeout", func(t *testing.T) {
		if DefaultTimeoutMS != 100 {
			t.Fatalf("DefaultTimeoutMS = %d, want 100", DefaultTimeoutMS)
		}
	})
	t.Run("timeout_must_be_positive", func(t *testing.T) {
		requireEvalError[*ValueError](t, "1 + 1", nil, "timeout_ms", Timeout(0))
	})
	t.Run("timeout_can_be_disabled", func(t *testing.T) {
		assertPyEqual(t, "1 + 1", mustEval(t, "1 + 1", nil, NoTimeout()), int64(2))
	})
	t.Run("timeout_exceeded_raises", func(t *testing.T) {
		base := time.Unix(0, 0)
		original := safeEvalClock
		safeEvalClock = fakeClock(base, base.Add(time.Second))
		t.Cleanup(func() { safeEvalClock = original })

		requireEvalError[*TimeoutError](t, "1 + 1", nil, "1ms", Timeout(1))
	})
}

// Parity-exempt: the two upstream tests assert SIGALRM/getitimer side effects
func TestSafeEvalTimeoutUsesDeadlinePolling(t *testing.T) {
	base := time.Unix(0, 0)
	original := safeEvalClock
	safeEvalClock = fakeClock(base, base, base.Add(2*time.Second))
	t.Cleanup(func() { safeEvalClock = original })

	requireEvalError[*TimeoutError](t, "1 + 1", nil, "5ms", Timeout(5))
}

// Parity: core/tests/test_safe_eval.py::TestUnaryOps::test_negation, core/tests/test_safe_eval.py::TestUnaryOps::test_positive, core/tests/test_safe_eval.py::TestUnaryOps::test_not_true, core/tests/test_safe_eval.py::TestUnaryOps::test_not_false, core/tests/test_safe_eval.py::TestUnaryOps::test_bitwise_invert
func TestSafeEvalUnaryOps(t *testing.T) {
	t.Run("negation", func(t *testing.T) {
		assertPyEqual(t, "-5", mustEval(t, "-5", nil), int64(-5))
	})
	t.Run("positive", func(t *testing.T) {
		assertPyEqual(t, "+5", mustEval(t, "+5", nil), int64(5))
	})
	t.Run("not_true", func(t *testing.T) {
		assertBool(t, "not True", mustEval(t, "not True", nil), false)
	})
	t.Run("not_false", func(t *testing.T) {
		assertBool(t, "not False", mustEval(t, "not False", nil), true)
	})
	t.Run("bitwise_invert", func(t *testing.T) {
		assertPyEqual(t, "~0", mustEval(t, "~0", nil), int64(-1))
	})
}

// Parity: core/tests/test_safe_eval.py::TestComparisons::test_equal, core/tests/test_safe_eval.py::TestComparisons::test_not_equal, core/tests/test_safe_eval.py::TestComparisons::test_less_than, core/tests/test_safe_eval.py::TestComparisons::test_greater_than, core/tests/test_safe_eval.py::TestComparisons::test_less_equal, core/tests/test_safe_eval.py::TestComparisons::test_greater_equal, core/tests/test_safe_eval.py::TestComparisons::test_is_none, core/tests/test_safe_eval.py::TestComparisons::test_is_not_none, core/tests/test_safe_eval.py::TestComparisons::test_in_list, core/tests/test_safe_eval.py::TestComparisons::test_not_in_list, core/tests/test_safe_eval.py::TestComparisons::test_chained_comparison, core/tests/test_safe_eval.py::TestComparisons::test_chained_comparison_false, core/tests/test_safe_eval.py::TestComparisons::test_chained_three_way
func TestSafeEvalComparisons(t *testing.T) {
	t.Run("equal", func(t *testing.T) {
		assertBool(t, "1 == 1", mustEval(t, "1 == 1", nil), true)
	})
	t.Run("not_equal", func(t *testing.T) {
		assertBool(t, "1 != 2", mustEval(t, "1 != 2", nil), true)
	})
	t.Run("less_than", func(t *testing.T) {
		assertBool(t, "1 < 2", mustEval(t, "1 < 2", nil), true)
	})
	t.Run("greater_than", func(t *testing.T) {
		assertBool(t, "2 > 1", mustEval(t, "2 > 1", nil), true)
	})
	t.Run("less_equal", func(t *testing.T) {
		assertBool(t, "2 <= 2", mustEval(t, "2 <= 2", nil), true)
	})
	t.Run("greater_equal", func(t *testing.T) {
		assertBool(t, "3 >= 2", mustEval(t, "3 >= 2", nil), true)
	})
	t.Run("is_none", func(t *testing.T) {
		assertBool(t, "x is None", mustEval(t, "x is None", map[string]any{"x": nil}), true)
	})
	t.Run("is_not_none", func(t *testing.T) {
		assertBool(t, "x is not None", mustEval(t, "x is not None", map[string]any{"x": 42}), true)
	})
	t.Run("in_list", func(t *testing.T) {
		assertBool(t, "'a' in x", mustEval(t, "'a' in x", map[string]any{"x": []any{"a", "b", "c"}}), true)
	})
	t.Run("not_in_list", func(t *testing.T) {
		assertBool(t, "'z' not in x", mustEval(t, "'z' not in x", map[string]any{"x": []any{"a", "b"}}), true)
	})
	t.Run("chained_comparison", func(t *testing.T) {
		assertBool(t, "1 < x < 10", mustEval(t, "1 < x < 10", map[string]any{"x": 5}), true)
	})
	t.Run("chained_comparison_false", func(t *testing.T) {
		assertBool(t, "1 < x < 3", mustEval(t, "1 < x < 3", map[string]any{"x": 5}), false)
	})
	t.Run("chained_three_way", func(t *testing.T) {
		assertBool(t, "0 <= x <= 100", mustEval(t, "0 <= x <= 100", map[string]any{"x": 50}), true)
	})
}

// Parity: core/tests/test_safe_eval.py::TestBooleanOps::test_and_true, core/tests/test_safe_eval.py::TestBooleanOps::test_and_false, core/tests/test_safe_eval.py::TestBooleanOps::test_or_true, core/tests/test_safe_eval.py::TestBooleanOps::test_or_false, core/tests/test_safe_eval.py::TestBooleanOps::test_and_returns_last_truthy, core/tests/test_safe_eval.py::TestBooleanOps::test_and_returns_first_falsy, core/tests/test_safe_eval.py::TestBooleanOps::test_or_returns_first_truthy, core/tests/test_safe_eval.py::TestBooleanOps::test_or_returns_last_falsy, core/tests/test_safe_eval.py::TestBooleanOps::test_and_short_circuits, core/tests/test_safe_eval.py::TestBooleanOps::test_or_short_circuits, core/tests/test_safe_eval.py::TestBooleanOps::test_and_guard_pattern_truthy, core/tests/test_safe_eval.py::TestBooleanOps::test_multi_and, core/tests/test_safe_eval.py::TestBooleanOps::test_multi_or, core/tests/test_safe_eval.py::TestBooleanOps::test_mixed_and_or
func TestSafeEvalBooleanOps(t *testing.T) {
	t.Run("and_true", func(t *testing.T) {
		assertBool(t, "True and True", mustEval(t, "True and True", nil), true)
	})
	t.Run("and_false", func(t *testing.T) {
		assertBool(t, "True and False", mustEval(t, "True and False", nil), false)
	})
	t.Run("or_true", func(t *testing.T) {
		assertBool(t, "False or True", mustEval(t, "False or True", nil), true)
	})
	t.Run("or_false", func(t *testing.T) {
		assertBool(t, "False or False", mustEval(t, "False or False", nil), false)
	})
	t.Run("and_returns_last_truthy", func(t *testing.T) {
		assertPyEqual(t, "1 and 2 and 3", mustEval(t, "1 and 2 and 3", nil), int64(3))
	})
	t.Run("and_returns_first_falsy", func(t *testing.T) {
		assertPyEqual(t, "1 and 0 and 3", mustEval(t, "1 and 0 and 3", nil), int64(0))
	})
	t.Run("or_returns_first_truthy", func(t *testing.T) {
		assertPyEqual(t, "0 or '' or 42", mustEval(t, "0 or '' or 42", nil), int64(42))
	})
	t.Run("or_returns_last_falsy", func(t *testing.T) {
		assertNil(t, "0 or '' or None", mustEval(t, "0 or '' or None", nil))
	})
	t.Run("and_short_circuits", func(t *testing.T) {
		assertBool(t, "x is not None and x.get('key')", mustEval(t, "x is not None and x.get('key')", map[string]any{"x": nil}), false)
	})
	t.Run("or_short_circuits", func(t *testing.T) {
		got := mustEval(t, "x or y.get('missing')", map[string]any{"x": "found", "y": map[string]any{}})
		assertPyEqual(t, "x or y.get('missing')", got, "found")
	})
	t.Run("and_guard_pattern_truthy", func(t *testing.T) {
		got := mustEval(t, "x is not None and x.get('key')", map[string]any{"x": map[string]any{"key": "value"}})
		assertPyEqual(t, "x is not None and x.get('key')", got, "value")
	})
	t.Run("multi_and", func(t *testing.T) {
		assertBool(t, "True and True and True", mustEval(t, "True and True and True", nil), true)
	})
	t.Run("multi_or", func(t *testing.T) {
		assertBool(t, "False or False or True", mustEval(t, "False or False or True", nil), true)
	})
	t.Run("mixed_and_or", func(t *testing.T) {
		assertBool(t, "True or False and False", mustEval(t, "True or False and False", nil), true)
	})
}

// Parity: core/tests/test_safe_eval.py::TestTernary::test_ternary_true_branch, core/tests/test_safe_eval.py::TestTernary::test_ternary_false_branch, core/tests/test_safe_eval.py::TestTernary::test_ternary_with_context, core/tests/test_safe_eval.py::TestTernary::test_ternary_false_with_context
func TestSafeEvalTernary(t *testing.T) {
	t.Run("true_branch", func(t *testing.T) {
		assertPyEqual(t, "'yes' if True else 'no'", mustEval(t, "'yes' if True else 'no'", nil), "yes")
	})
	t.Run("false_branch", func(t *testing.T) {
		assertPyEqual(t, "'yes' if False else 'no'", mustEval(t, "'yes' if False else 'no'", nil), "no")
	})
	t.Run("with_context", func(t *testing.T) {
		assertPyEqual(t, "x * 2 if x > 0 else -x", mustEval(t, "x * 2 if x > 0 else -x", map[string]any{"x": 5}), int64(10))
	})
	t.Run("false_with_context", func(t *testing.T) {
		assertPyEqual(t, "x * 2 if x > 0 else -x", mustEval(t, "x * 2 if x > 0 else -x", map[string]any{"x": -3}), int64(3))
	})
}

// Parity: core/tests/test_safe_eval.py::TestVariables::test_simple_variable, core/tests/test_safe_eval.py::TestVariables::test_string_variable, core/tests/test_safe_eval.py::TestVariables::test_dict_variable, core/tests/test_safe_eval.py::TestVariables::test_undefined_variable_raises, core/tests/test_safe_eval.py::TestVariables::test_multiple_variables
func TestSafeEvalVariables(t *testing.T) {
	t.Run("simple_variable", func(t *testing.T) {
		assertPyEqual(t, "x", mustEval(t, "x", map[string]any{"x": 42}), int64(42))
	})
	t.Run("string_variable", func(t *testing.T) {
		assertPyEqual(t, "name", mustEval(t, "name", map[string]any{"name": "Alice"}), "Alice")
	})
	t.Run("dict_variable", func(t *testing.T) {
		got := mustEval(t, "output", map[string]any{"output": map[string]any{"status": "ok"}})
		assertPyEqual(t, "output", got, map[string]any{"status": "ok"})
	})
	t.Run("undefined_variable_raises", func(t *testing.T) {
		requireEvalError[*NameError](t, "undefined_var", nil, "not defined")
	})
	t.Run("multiple_variables", func(t *testing.T) {
		assertPyEqual(t, "x + y", mustEval(t, "x + y", map[string]any{"x": 10, "y": 20}), int64(30))
	})
}

// Parity: core/tests/test_safe_eval.py::TestSubscript::test_dict_subscript, core/tests/test_safe_eval.py::TestSubscript::test_list_subscript, core/tests/test_safe_eval.py::TestSubscript::test_nested_subscript, core/tests/test_safe_eval.py::TestSubscript::test_missing_key_raises
func TestSafeEvalSubscript(t *testing.T) {
	t.Run("dict_subscript", func(t *testing.T) {
		got := mustEval(t, "d['key']", map[string]any{"d": map[string]any{"key": "value"}})
		assertPyEqual(t, "d['key']", got, "value")
	})
	t.Run("list_subscript", func(t *testing.T) {
		got := mustEval(t, "items[0]", map[string]any{"items": []any{10, 20, 30}})
		assertPyEqual(t, "items[0]", got, int64(10))
	})
	t.Run("nested_subscript", func(t *testing.T) {
		ctx := map[string]any{"data": map[string]any{"users": []any{map[string]any{"name": "Alice"}}}}
		assertPyEqual(t, "data['users'][0]['name']", mustEval(t, "data['users'][0]['name']", ctx), "Alice")
	})
	t.Run("missing_key_raises", func(t *testing.T) {
		requireEvalError[*KeyError](t, "d['missing']", map[string]any{"d": map[string]any{}}, "")
	})
}

// Parity: core/tests/test_safe_eval.py::TestAttributeAccess::test_private_attr_blocked, core/tests/test_safe_eval.py::TestAttributeAccess::test_dunder_blocked, core/tests/test_safe_eval.py::TestAttributeAccess::test_single_underscore_blocked
func TestSafeEvalAttributeAccess(t *testing.T) {
	t.Run("private_attr_blocked", func(t *testing.T) {
		requireEvalError[*ValueError](t, "x.__class__", map[string]any{"x": 42}, "private attribute")
	})
	t.Run("dunder_blocked", func(t *testing.T) {
		requireEvalError[*ValueError](t, "x.__dict__", map[string]any{"x": map[string]any{}}, "private attribute")
	})
	t.Run("single_underscore_blocked", func(t *testing.T) {
		requireEvalError[*ValueError](t, "x._internal", map[string]any{"x": map[string]any{}}, "private attribute")
	})
}

// Parity: core/tests/test_safe_eval.py::TestFunctionCalls::test_len, core/tests/test_safe_eval.py::TestFunctionCalls::test_int_conversion, core/tests/test_safe_eval.py::TestFunctionCalls::test_float_conversion, core/tests/test_safe_eval.py::TestFunctionCalls::test_str_conversion, core/tests/test_safe_eval.py::TestFunctionCalls::test_bool_conversion, core/tests/test_safe_eval.py::TestFunctionCalls::test_abs, core/tests/test_safe_eval.py::TestFunctionCalls::test_min, core/tests/test_safe_eval.py::TestFunctionCalls::test_max, core/tests/test_safe_eval.py::TestFunctionCalls::test_sum, core/tests/test_safe_eval.py::TestFunctionCalls::test_round, core/tests/test_safe_eval.py::TestFunctionCalls::test_all, core/tests/test_safe_eval.py::TestFunctionCalls::test_any, core/tests/test_safe_eval.py::TestFunctionCalls::test_list_constructor, core/tests/test_safe_eval.py::TestFunctionCalls::test_dict_constructor, core/tests/test_safe_eval.py::TestFunctionCalls::test_tuple_constructor, core/tests/test_safe_eval.py::TestFunctionCalls::test_set_constructor
func TestSafeEvalFunctionCalls(t *testing.T) {
	t.Run("len", func(t *testing.T) {
		got := mustEval(t, "len(x)", map[string]any{"x": []any{1, 2, 3}})
		assertPyEqual(t, "len(x)", got, int64(3))
	})
	t.Run("int_conversion", func(t *testing.T) {
		assertPyEqual(t, "int('42')", mustEval(t, "int('42')", nil), int64(42))
	})
	t.Run("float_conversion", func(t *testing.T) {
		assertPyApprox(t, "float('3.14')", mustEval(t, "float('3.14')", nil), 3.14)
	})
	t.Run("str_conversion", func(t *testing.T) {
		assertPyEqual(t, "str(42)", mustEval(t, "str(42)", nil), "42")
	})
	t.Run("bool_conversion", func(t *testing.T) {
		assertBool(t, "bool(1)", mustEval(t, "bool(1)", nil), true)
	})
	t.Run("abs", func(t *testing.T) {
		assertPyEqual(t, "abs(-5)", mustEval(t, "abs(-5)", nil), int64(5))
	})
	t.Run("min", func(t *testing.T) {
		assertPyEqual(t, "min(3, 1, 2)", mustEval(t, "min(3, 1, 2)", nil), int64(1))
	})
	t.Run("max", func(t *testing.T) {
		assertPyEqual(t, "max(3, 1, 2)", mustEval(t, "max(3, 1, 2)", nil), int64(3))
	})
	t.Run("sum", func(t *testing.T) {
		got := mustEval(t, "sum(x)", map[string]any{"x": []any{1, 2, 3}})
		assertPyEqual(t, "sum(x)", got, int64(6))
	})
	t.Run("round", func(t *testing.T) {
		assertPyEqual(t, "round(3.7)", mustEval(t, "round(3.7)", nil), int64(4))
	})
	t.Run("all", func(t *testing.T) {
		assertBool(t, "all([True, True, True])", mustEval(t, "all([True, True, True])", nil), true)
	})
	t.Run("any", func(t *testing.T) {
		assertBool(t, "any([False, False, True])", mustEval(t, "any([False, False, True])", nil), true)
	})
	t.Run("list_constructor", func(t *testing.T) {
		got := mustEval(t, "list(x)", map[string]any{"x": Tuple{1, 2, 3}})
		assertPyEqual(t, "list(x)", got, []any{1, 2, 3})
	})
	t.Run("dict_constructor", func(t *testing.T) {
		assertPyEqual(t, "dict(a=1, b=2)", mustEval(t, "dict(a=1, b=2)", nil), map[string]any{"a": 1, "b": 2})
	})
	t.Run("tuple_constructor", func(t *testing.T) {
		got := mustEval(t, "tuple(x)", map[string]any{"x": []any{1, 2}})
		assertPyEqual(t, "tuple(x)", got, Tuple{1, 2})
	})
	t.Run("set_constructor", func(t *testing.T) {
		got := mustEval(t, "set(x)", map[string]any{"x": []any{1, 2, 2, 3}})
		assertPyEqual(t, "set(x)", got, SetOf(1, 2, 3))
	})
}

// Parity: core/tests/test_safe_eval.py::TestMethodCalls::test_dict_get, core/tests/test_safe_eval.py::TestMethodCalls::test_dict_get_missing, core/tests/test_safe_eval.py::TestMethodCalls::test_dict_keys, core/tests/test_safe_eval.py::TestMethodCalls::test_dict_values, core/tests/test_safe_eval.py::TestMethodCalls::test_string_lower, core/tests/test_safe_eval.py::TestMethodCalls::test_string_upper, core/tests/test_safe_eval.py::TestMethodCalls::test_string_strip, core/tests/test_safe_eval.py::TestMethodCalls::test_string_split
func TestSafeEvalMethodCalls(t *testing.T) {
	t.Run("dict_get", func(t *testing.T) {
		got := mustEval(t, "d.get('key', 'default')", map[string]any{"d": map[string]any{"key": "val"}})
		assertPyEqual(t, "d.get('key', 'default')", got, "val")
	})
	t.Run("dict_get_missing", func(t *testing.T) {
		got := mustEval(t, "d.get('missing', 'default')", map[string]any{"d": map[string]any{}})
		assertPyEqual(t, "d.get('missing', 'default')", got, "default")
	})
	t.Run("dict_keys", func(t *testing.T) {
		got := mustEval(t, "list(d.keys())", map[string]any{"d": map[string]any{"a": 1, "b": 2}})
		assertStrings(t, sortedValues(t, "list(d.keys())", got), []string{"a", "b"})
	})
	t.Run("dict_values", func(t *testing.T) {
		got := mustEval(t, "list(d.values())", map[string]any{"d": map[string]any{"a": 1, "b": 2}})
		assertStrings(t, sortedValues(t, "list(d.values())", got), []string{"1", "2"})
	})
	t.Run("string_lower", func(t *testing.T) {
		got := mustEval(t, "s.lower()", map[string]any{"s": "HELLO"})
		assertPyEqual(t, "s.lower()", got, "hello")
	})
	t.Run("string_upper", func(t *testing.T) {
		got := mustEval(t, "s.upper()", map[string]any{"s": "hello"})
		assertPyEqual(t, "s.upper()", got, "HELLO")
	})
	t.Run("string_strip", func(t *testing.T) {
		got := mustEval(t, "s.strip()", map[string]any{"s": "  hi  "})
		assertPyEqual(t, "s.strip()", got, "hi")
	})
	t.Run("string_split", func(t *testing.T) {
		got := mustEval(t, "s.split(',')", map[string]any{"s": "a,b,c"})
		assertPyEqual(t, "s.split(',')", got, []any{"a", "b", "c"})
	})
}

// Parity: core/tests/test_safe_eval.py::TestSecurity::test_import_blocked, core/tests/test_safe_eval.py::TestSecurity::test_lambda_blocked, core/tests/test_safe_eval.py::TestSecurity::test_comprehension_blocked, core/tests/test_safe_eval.py::TestSecurity::test_assignment_blocked, core/tests/test_safe_eval.py::TestSecurity::test_disallowed_function_blocked, core/tests/test_safe_eval.py::TestSecurity::test_exec_blocked, core/tests/test_safe_eval.py::TestSecurity::test_type_call_blocked, core/tests/test_safe_eval.py::TestSecurity::test_getattr_builtin_blocked, core/tests/test_safe_eval.py::TestSecurity::test_empty_expression_raises
func TestSafeEvalSecurity(t *testing.T) {
	t.Run("import_blocked", func(t *testing.T) {
		requireEvalError[*NameError](t, "__import__('os')", nil, "not defined")
	})
	t.Run("lambda_blocked", func(t *testing.T) {
		requireEvalError[*ValueError](t, "(lambda: 1)()", nil, "not allowed")
	})
	t.Run("comprehension_blocked", func(t *testing.T) {
		requireEvalError[*ValueError](t, "[x for x in range(10)]", nil, "not allowed")
	})
	t.Run("assignment_blocked", func(t *testing.T) {
		requireEvalError[*SyntaxError](t, "x = 5", nil, "")
	})
	t.Run("disallowed_function_blocked", func(t *testing.T) {
		requireEvalError[*NameError](t, "eval('1+1')", nil, "not defined")
	})
	t.Run("exec_blocked", func(t *testing.T) {
		requireEvalError[*NameError](t, "exec('x=1')", nil, "not defined")
	})
	t.Run("type_call_blocked", func(t *testing.T) {
		requireEvalError[*NameError](t, "type(42)", nil, "not defined")
	})
	t.Run("getattr_builtin_blocked", func(t *testing.T) {
		requireEvalError[*NameError](t, "getattr(x, '__class__')", map[string]any{"x": 42}, "not defined")
	})
	t.Run("empty_expression_raises", func(t *testing.T) {
		requireEvalError[*SyntaxError](t, "", nil, "")
	})
}

// Parity: core/tests/test_safe_eval.py::TestEdgeConditionPatterns::test_output_key_exists_and_not_none, core/tests/test_safe_eval.py::TestEdgeConditionPatterns::test_output_key_missing, core/tests/test_safe_eval.py::TestEdgeConditionPatterns::test_output_key_check_with_fallback, core/tests/test_safe_eval.py::TestEdgeConditionPatterns::test_guard_then_length_check, core/tests/test_safe_eval.py::TestEdgeConditionPatterns::test_guard_short_circuits_on_none, core/tests/test_safe_eval.py::TestEdgeConditionPatterns::test_success_flag_check, core/tests/test_safe_eval.py::TestEdgeConditionPatterns::test_buffer_threshold, core/tests/test_safe_eval.py::TestEdgeConditionPatterns::test_string_contains_check, core/tests/test_safe_eval.py::TestEdgeConditionPatterns::test_fallback_chain, core/tests/test_safe_eval.py::TestEdgeConditionPatterns::test_no_context_needed
func TestSafeEvalEdgeConditionPatterns(t *testing.T) {
	t.Run("output_key_exists_and_not_none", func(t *testing.T) {
		ctx := map[string]any{"output": map[string]any{"approved_contacts": []any{"alice@example.com"}}}
		assertBool(t, "output.get('approved_contacts') is not None", mustEval(t, "output.get('approved_contacts') is not None", ctx), true)
	})
	t.Run("output_key_missing", func(t *testing.T) {
		ctx := map[string]any{"output": map[string]any{}}
		assertBool(t, "output.get('approved_contacts') is not None", mustEval(t, "output.get('approved_contacts') is not None", ctx), false)
	})
	t.Run("output_key_check_with_fallback", func(t *testing.T) {
		ctx := map[string]any{"output": map[string]any{"redo_extraction": true}}
		assertBool(t, "output.get('redo_extraction') is not None", mustEval(t, "output.get('redo_extraction') is not None", ctx), true)
	})
	t.Run("guard_then_length_check", func(t *testing.T) {
		ctx := map[string]any{"output": map[string]any{"results": []any{1, 2, 3}}}
		expression := "output.get('results') is not None and len(output['results']) > 0"
		assertBool(t, expression, mustEval(t, expression, ctx), true)
	})
	t.Run("guard_short_circuits_on_none", func(t *testing.T) {
		ctx := map[string]any{"output": map[string]any{}}
		expression := "output.get('results') is not None and len(output['results']) > 0"
		assertBool(t, expression, mustEval(t, expression, ctx), false)
	})
	t.Run("success_flag_check", func(t *testing.T) {
		ctx := map[string]any{"output": map[string]any{"success": true}, "buffer": map[string]any{"attempts": 2}}
		assertBool(t, "output.get('success') == True", mustEval(t, "output.get('success') == True", ctx), true)
	})
	t.Run("buffer_threshold", func(t *testing.T) {
		ctx := map[string]any{"buffer": map[string]any{"score": 0.85}}
		assertBool(t, "buffer.get('score', 0) >= 0.8", mustEval(t, "buffer.get('score', 0) >= 0.8", ctx), true)
	})
	t.Run("string_contains_check", func(t *testing.T) {
		ctx := map[string]any{"output": map[string]any{"status": "completed_with_warnings"}}
		expression := "'completed' in output.get('status', '')"
		assertBool(t, expression, mustEval(t, expression, ctx), true)
	})
	t.Run("fallback_chain", func(t *testing.T) {
		ctx := map[string]any{"output": map[string]any{}}
		expression := "output.get('primary') or output.get('secondary') or 'default'"
		result := mustEval(t, expression, ctx)
		assertPyEqual(t, expression, result, "default")
	})
	t.Run("no_context_needed", func(t *testing.T) {
		assertBool(t, "True", mustEval(t, "True", nil), true)
		assertBool(t, "1 == 1", mustEval(t, "1 == 1", nil), true)
	})
}
