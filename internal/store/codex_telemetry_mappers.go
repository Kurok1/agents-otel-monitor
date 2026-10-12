/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.2.0
 */

package store

import (
	"database/sql/driver"
	"fmt"

	"github.com/kuroky/claude-code-monitor/internal/otlp"
)

func mapCodexSkillInvocation(row any) ([]driver.Value, error) {
	r, ok := row.(otlp.CodexEventSkillInvocationRow)
	if !ok {
		return nil, fmt.Errorf("expected CodexEventSkillInvocationRow, got %T", row)
	}
	attrs, err := attrsValue(r.Attrs)
	if err != nil {
		return nil, err
	}
	return append(commonCodexEventCols(r.CodexCommonAttrs), nullStr(r.TurnID), nullStr(r.SkillName), nullStr(r.Scope), nullStr(r.PluginID), nullStr(r.InvocationType), attrs), nil
}

func mapCodexTurnCost(row any) ([]driver.Value, error) {
	r, ok := row.(otlp.CodexEventTurnCostRow)
	if !ok {
		return nil, fmt.Errorf("expected CodexEventTurnCostRow, got %T", row)
	}
	attrs, err := attrsValue(r.Attrs)
	if err != nil {
		return nil, err
	}
	return append(commonCodexEventCols(r.CodexCommonAttrs), nullStr(r.TurnID), nullStr(r.EstimatedUSD), nullBool(r.Interrupted), nullStr(r.Speed), nullStr(r.ReasoningEffort), attrs), nil
}

func mapCodexTurnTokenUsage(row any) ([]driver.Value, error) {
	r, ok := row.(otlp.CodexMetricTurnTokenUsageRow)
	if !ok {
		return nil, fmt.Errorf("expected CodexMetricTurnTokenUsageRow, got %T", row)
	}
	attrs, err := attrsValue(r.Attrs)
	if err != nil {
		return nil, err
	}
	return append(commonCodexMetricCols(r.CodexCommonAttrs, r.StartTimestamp, r.SampleCount, r.SumTokens), nullStr(r.TokenType), nullStr(r.SessionSource), attrs), nil
}
