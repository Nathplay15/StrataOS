// Ranking of the global search (no imports: tested by
// node --experimental-strip-types frontend/scripts/search-rank-test.ts).

export interface Searchable {
  /** Names shown or known for the item (translated and English); the first is the main one. */
  labels: string[];
  /** Extra words that find the item (less weight than its names). */
  keywords?: string[];
}

/** Lower case without accents, for matching "réseau" with "reseau". */
export function norm(s: string): string {
  return s.toLowerCase().normalize("NFD").replace(/[̀-ͯ]/g, "").trim();
}

/** How well one word of the query matches a text: 0 = not at all. */
function wordScore(word: string, text: string): number {
  if (!text) return 0;
  if (text === word) return 100;
  if (text.startsWith(word)) return 80;
  if (text.split(/[\s/()·,.:_-]+/).some((w) => w.startsWith(word))) return 60;
  if (text.includes(word)) return 30;
  return 0;
}

/**
 * Score of an item for a query: every word of the query must match a name or
 * a keyword; names count more than keywords, a whole-name match more than a
 * prefix, a prefix more than the inside of a word. 0 = not a result.
 */
export function score(query: string, item: Searchable): number {
  const q = norm(query);
  if (!q) return 0;
  const labels = item.labels.map(norm);
  const keywords = (item.keywords ?? []).map(norm);
  let total = 0;
  for (const word of q.split(/\s+/)) {
    let best = 0;
    for (const l of labels) best = Math.max(best, wordScore(word, l));
    for (const k of keywords) best = Math.max(best, wordScore(word, k) * 0.6);
    if (best === 0) return 0;
    total += best;
  }
  // The whole query matching a name beats words found here and there.
  const whole = Math.max(...labels.map((l) => wordScore(q, l)));
  const shortest = Math.min(...labels.map((l) => l.length));
  return total + whole - Math.min(shortest, 40) * 0.1;
}

/** Items matching the query, best first (ties keep the given order), at most limit. */
export function rank<T extends Searchable>(query: string, items: T[], limit = Infinity): T[] {
  return items
    .map((item, i) => ({ item, i, s: score(query, item) }))
    .filter((x) => x.s > 0)
    .sort((a, b) => b.s - a.s || a.i - b.i)
    .slice(0, limit)
    .map((x) => x.item);
}
