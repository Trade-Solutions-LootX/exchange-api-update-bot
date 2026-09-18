package analyze

import (
	"fmt"
	"strings"

	"exchangebot/internal/model"
)

// systemPrompt tells the model what the terminal is and what "important" means
// for it. Kept in Russian to match the task/Telegram output language.
const systemPrompt = `Ты — инженер интеграций торгового терминала LootX (десктопный терминал: C#/.NET, коннекторы к биржам Binance, Bybit, OKX, Bitget, KuCoin, BingX, MEXC, Gate, HTX, Hyperliquid, Aster, Lighter по REST и WebSocket: котировки, стакан, сделки, свечи, размещение/отмена ордеров, позиции, баланс, listenKey/приватные потоки, подпись запросов, лимиты запросов, список инструментов и их параметры — шаг цены/объёма, плечо, режимы маржи).

Тебе дают одно объявление биржи (заголовок, превью из ленты, текст страницы если удалось скачать). Определи, касается ли оно публичного торгового API/подключения и должен ли терминал что-то менять, чтобы продолжать корректно работать.

Считай важным для терминала (needs_terminal_change=true):
- deprecation/удаление/переименование эндпоинтов, полей, каналов WebSocket, изменение формата ответа или ошибок;
- изменения аутентификации/подписи, listenKey, доменов/URL, TLS, версий API, обязательных заголовков;
- изменения лимитов запросов (rate limit, weight), правил ордеров (типы, timeInForce, precision, minNotional, reduce-only, position mode);
- делистинг/переименование инструментов, изменение параметров контрактов, если это ломает торговлю по ним из терминала;
- изменения market data (частота, глубина, схема стакана/чексуммы, агрегированные сделки);
- миграции (unified account, новый API v5 и т.п.) с дедлайном.

НЕ важно (needs_terminal_change=false): листинги/промо/акции/earn/launchpool, маркетинг, конкурсы, новости компании, изменения только в веб-интерфейсе или мобильном приложении, фиатные каналы, если они не влияют на торговый API.

importance: critical — терминал перестанет работать/торговать или дедлайн ≤ 30 дней; high — нужны изменения кода до дедлайна или заметная деградация; medium — стоит проверить/обновить без срочности; low — информационно; none — не касается API.

Пиши по-русски, конкретно и проверяемо: в changes — что именно меняется (эндпоинты, поля, каналы, даты), в actions — что сделать в коде терминала (какой коннектор/слой, что проверить, чем заменить). Не выдумывай деталей, которых нет в тексте: если текст страницы не удалось получить и превью скудное — так и скажи в summary и снизь уверенность (importance не выше medium, если суть не ясна).

Ответ — строго один JSON-объект:
{
  "api_related": boolean,
  "importance": "none" | "low" | "medium" | "high" | "critical",
  "needs_terminal_change": boolean,
  "summary": "2–4 предложения: что изменилось и чем грозит терминалу",
  "changes": ["конкретное изменение", "..."],
  "actions": ["что сделать в терминале", "..."],
  "affected": ["rest" | "websocket" | "auth" | "rate-limit" | "orders" | "market-data" | "symbols" | "futures" | "spot" | "margin" | "options" | "account"],
  "effective_at": "YYYY-MM-DD или пусто",
  "task_title": "короткий заголовок задачи, до 12 слов"
}`

func buildUserPrompt(ann model.Announcement, article string, maxArticle int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Биржа: %s\nИсточник: %s\nЗаголовок: %s\nURL: %s\n", strings.ToUpper(ann.Exchange), ann.Source, ann.Title, ann.URL)
	if !ann.PublishedAt.IsZero() {
		fmt.Fprintf(&sb, "Опубликовано: %s\n", ann.PublishedAt.UTC().Format("2006-01-02 15:04 UTC"))
	}
	if len(ann.Markets) > 0 {
		fmt.Fprintf(&sb, "Рынки по классификатору: %s\n", ann.MarketsString())
	}
	if len(ann.MatchedRules) > 0 {
		fmt.Fprintf(&sb, "Сработавшие правила классификатора: %s (эвристика, не истина)\n", strings.Join(ann.MatchedRules, ", "))
	}
	body := strings.TrimSpace(ann.Body)
	if body != "" {
		fmt.Fprintf(&sb, "\nПревью из ленты:\n%s\n", truncate(body, 3000))
	}
	if article != "" {
		fmt.Fprintf(&sb, "\nТекст страницы анонса:\n%s\n", truncate(article, maxArticle))
	} else {
		sb.WriteString("\nТекст страницы получить не удалось — оценивай по заголовку и превью.\n")
	}
	return sb.String()
}
