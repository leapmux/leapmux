/**
 * Wrap `compute` so that it runs at most once for each key object, and return the
 * wrapper.
 *
 * The key object stands for one revision of its data: a caller that changes the data
 * builds a new object, and the new object misses the cache. A key object must
 * therefore never change after its first read. The cache holds each key weakly, so an
 * entry lives exactly as long as its key.
 *
 * Every answer is cached, `null` and `undefined` included, because the wrapper tests
 * the key with `has()` and not the answer.
 */
export function memoizeByObject<K extends object, V>(compute: (key: K) => V): (key: K) => V {
  const answers = new WeakMap<K, V>()
  return (key) => {
    if (answers.has(key))
      return answers.get(key) as V
    const answer = compute(key)
    answers.set(key, answer)
    return answer
  }
}
