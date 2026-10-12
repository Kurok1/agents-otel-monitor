/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.2.0
 */

package dashboard

import (
	"context"
	"fmt"
)

func queryCodexSessionSkills(ctx context.Context, db sqlQueryer, conversationID string) ([]SkillRank, error) {
	rows, err := db.QueryContext(ctx, `SELECT skill_name, COUNT(*),
 COUNT(*) FILTER (WHERE invocation_type='explicit'), COUNT(*) FILTER (WHERE invocation_type='implicit')
 FROM codex_skill_invocations WHERE conversation_id=? GROUP BY skill_name ORDER BY COUNT(*) DESC, skill_name`, conversationID)
	if err != nil {
		return nil, fmt.Errorf("query codex session skills: %w", err)
	}
	defer rows.Close()
	result := make([]SkillRank, 0)
	for rows.Next() {
		var r SkillRank
		if err := rows.Scan(&r.Name, &r.Activations, &r.Explicit, &r.Implicit); err != nil {
			return nil, fmt.Errorf("scan codex session skill: %w", err)
		}
		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate codex session skills: %w", err)
	}
	return result, nil
}
