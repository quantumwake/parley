import { describe, expect, it } from 'vitest'
import { filterKinds, mentionsIn, parseComposer, replaceToken, tokenAt } from './composer'

describe('parseComposer', () => {
  it('defaults to comment', () => {
    expect(parseComposer('hello')).toEqual({ kind: 'comment', text: 'hello', sigil: '', to: '' })
  })
  it('reads /kind and :kind', () => {
    expect(parseComposer('/question who owns this?')).toEqual({ kind: 'question', text: 'who owns this?', sigil: '/', to: '' })
    expect(parseComposer(':status shipped')).toEqual({ kind: 'status', text: 'shipped', sigil: ':', to: '' })
  })
  it('reads /everyone and :everyone', () => {
    expect(parseComposer('/everyone please look')).toEqual({ kind: 'comment', text: 'please look', sigil: '', to: 'everyone' })
    expect(parseComposer(':everyone /question who?')).toEqual({ kind: 'question', text: 'who?', sigil: '/', to: 'everyone' })
  })
  it('ignores unknown /tokens', () => {
    expect(parseComposer('/nope still comment')).toEqual({ kind: 'comment', text: '/nope still comment', sigil: '', to: '' })
  })
})

describe('mentionsIn', () => {
  it('collects @names', () => {
    expect(mentionsIn('hey @alice and @bob')).toEqual(['alice', 'bob'])
    expect(mentionsIn('@* everyone')).toEqual(['everyone'])
    expect(mentionsIn('@everyone look')).toEqual(['everyone'])
  })
  it('does not keep trailing punctuation on a mention', () => {
    expect(mentionsIn('@alice, can you look?')).toEqual(['alice'])
    expect(mentionsIn('see @bob.')).toEqual(['bob'])
  })
})

describe('tokenAt', () => {
  it('finds a leading kind token', () => {
    expect(tokenAt('/que', 4)).toEqual({ sigil: '/', query: 'que', start: 0, end: 4 })
    expect(tokenAt('hello /no', 9)).toBeNull()
  })
  it('finds @ in the middle', () => {
    expect(tokenAt('hi @al', 6)).toEqual({ sigil: '@', query: 'al', start: 3, end: 6 })
  })
})

describe('replaceToken', () => {
  it('completes the token', () => {
    expect(replaceToken('/que more', tokenAt('/que more', 4), '/question')).toBe('/question more')
  })
})

describe('filterKinds', () => {
  it('prefixes', () => {
    expect(filterKinds('stat').map((k) => k.id)).toEqual(['status'])
    expect(filterKinds('q').map((k) => k.id)).toEqual(['question'])
  })
})
