package analyze

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"exchangebot/internal/model"
)

// Cross-source dedup. The same API change usually shows up several times:
// the exchange's announcement, its API-updates feed, a docs-repo commit, an
// SDK release. Each is a different announcement (different DedupKey), so the
// per-announcement `task:` key does not help. Before creating a task we ask
// the model whether an OPEN task with our tag already covers the change; if
// so, the new source is appended to that task as a comment (link + what the
// new source adds) instead of opening a second one.

const dedupSystemPrompt = `Ты сверяешь новое изменение API биржи со списком уже открытых задач в трекере. Задача считается дубликатом, если она про ТО ЖЕ САМОЕ изменение (тот же эндпоинт/канал/правило/миграция той же биржи), даже если сформулировано иначе или пришло из другого источника (анонс, changelog, коммит в документации). Разные изменения одной биржи — не дубликаты. Другое поле того же эндпоинта с другой датой — не дубликат.

Ответ — строго один JSON-объект:
{"duplicate_of": "<id задачи или null>", "confidence": 0.0–1.0, "reason": "одно предложение"}`

type dedupVerdict struct {
	DuplicateOf *string `json:"duplicate_of"`
	Confidence  float64 `json:"confidence"`
	Reason      string  `json:"reason"`
}

// dedupMinConfidence: below this the model's "duplicate" is ignored and a
// new task is created — a missed merge costs a duplicate card, a wrong merge
// hides a real change.
const dedupMinConfidence = 0.7

// maxDedupCandidates bounds the prompt: the newest open tasks of the same
// exchange are the only plausible matches.
const maxDedupCandidates = 30

// findDuplicate returns the open task that already covers this change, or
// nil. Errors are logged by the caller and treated as "no duplicate".
func (a *Analyzer) findDuplicate(ctx context.Context, ann model.Announcement, v *Verdict) (*Task, error) {
	open, err := a.clickup.ListOpenTagged(ctx)
	if err != nil {
		return nil, err
	}
	prefix := "[" + strings.ToUpper(ann.Exchange) + "]"
	var cands []Task
	for _, t := range open {
		if strings.HasPrefix(strings.ToUpper(t.Name), prefix) {
			cands = append(cands, t)
		}
		if len(cands) >= maxDedupCandidates {
			break
		}
	}
	if len(cands) == 0 {
		return nil, nil
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "НОВОЕ ИЗМЕНЕНИЕ (%s)\nЗаголовок: %s\nИсточник: %s\nСуть: %s\n", strings.ToUpper(ann.Exchange), ann.Title, ann.Source, v.Summary)
	if len(v.Changes) > 0 {
		sb.WriteString("Изменения:\n" + bullets(v.Changes) + "\n")
	}
	if v.EffectiveAt != "" {
		sb.WriteString("Вступает: " + v.EffectiveAt + "\n")
	}
	sb.WriteString("\nОТКРЫТЫЕ ЗАДАЧИ:\n")
	for _, t := range cands {
		fmt.Fprintf(&sb, "\n--- id: %s\nназвание: %s\n%s\n", t.ID, t.Name, truncate(strings.TrimSpace(t.Text), 900))
	}
	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	raw, err := a.llm.Complete(cctx, dedupSystemPrompt, sb.String(), 600)
	if err != nil {
		return nil, err
	}
	var dv dedupVerdict
	if err := json.Unmarshal([]byte(extractJSON(raw)), &dv); err != nil {
		return nil, fmt.Errorf("dedup verdict: %w", err)
	}
	if dv.DuplicateOf == nil || *dv.DuplicateOf == "" || *dv.DuplicateOf == "null" || dv.Confidence < dedupMinConfidence {
		return nil, nil
	}
	for i := range cands {
		if cands[i].ID == *dv.DuplicateOf {
			a.log.Info("duplicate task detected", "exchange", ann.Exchange, "title", ann.Title, "task", cands[i].Name, "confidence", dv.Confidence, "reason", dv.Reason)
			return &cands[i], nil
		}
	}
	return nil, nil
}

// mergeComment is what gets appended to the existing task: where else the
// change was seen and what this source adds. Plain text — ClickUp's
// comment_text is not markdown.
func mergeComment(ann model.Announcement, v *Verdict) string {
	when := ""
	if !ann.PublishedAt.IsZero() {
		when = " · " + ann.PublishedAt.UTC().Format("2006-01-02 15:04 UTC")
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Ещё один источник того же изменения: %s%s\n%s\n", ann.Title, when, ann.URL)
	fmt.Fprintf(&sb, "(источник %s, оценка модели: %s)\n\n", ann.Source, v.Importance)
	if s := strings.TrimSpace(v.Summary); s != "" {
		sb.WriteString("Что изменилось по этому источнику:\n" + s + "\n")
	}
	if len(v.Changes) > 0 {
		sb.WriteString("\n" + bullets(v.Changes) + "\n")
	}
	if len(v.Actions) > 0 {
		sb.WriteString("\nЧто сделать в терминале:\n" + bullets(v.Actions) + "\n")
	}
	if eff := strings.TrimSpace(v.EffectiveAt); eff != "" {
		sb.WriteString("\nВступает в силу: " + eff + "\n")
	}
	return truncate(sb.String(), 8000)
}
