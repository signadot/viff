#!/usr/bin/env python3
"""doc-readability.py — how heavy a page's prose is for a reader of English as a foreign language.

Counts prose only: fenced code, tables, headings and link targets are left out. For each page:
words, sentences, mean words per sentence, sentences over 30 and over 40 words, and dash asides
(an em dash, or a spaced hyphen used as one), each also per 1,000 words.

    scripts/doc-readability.py README.md docs/*.md docs/reference/*.md
"""
import re, sys

def prose(text):
    out, fence = [], False
    for line in text.split('\n'):
        if line.lstrip().startswith('```'):
            fence = not fence
            continue
        if fence or line.lstrip().startswith('|') or line.startswith('#') or line.startswith('    '):
            out.append('')
            continue
        out.append(line)
    t = '\n'.join(out)
    t = re.sub(r'`[^`]*`', 'CODE', t)                 # inline code is one word
    t = re.sub(r'\[([^\]]*)\]\([^)]*\)', r'\1', t)   # a link reads as its text
    t = re.sub(r'^\s*([-*]|\d+\.)\s+', '', t, flags=re.M)
    return t

def sentences(t):
    paras = re.split(r'\n\s*\n', t)
    for p in paras:
        p = ' '.join(p.split())
        if not p:
            continue
        for s in re.split(r'(?<=[.!?])\s+(?=[A-Z`"(\[*])', p):
            n = len(re.findall(r"[A-Za-z0-9][A-Za-z0-9'’-]*", s))
            if n >= 3:
                yield n, s

def measure(path):
    t = prose(open(path).read())
    ss = list(sentences(t))
    words = sum(n for n, _ in ss)
    if not ss:
        return None
    dashes = len(re.findall(r'—| -- ', t))
    return dict(path=path, words=words, sents=len(ss), mean=words/len(ss),
                over30=sum(1 for n, _ in ss if n > 30), over40=sum(1 for n, _ in ss if n > 40),
                dashes=dashes, longest=max(ss)[0])

if __name__ == '__main__':
    rows = [m for m in (measure(p) for p in sys.argv[1:]) if m]
    print(f"{'page':48} {'words':>6} {'mean':>5} {'>30':>4} {'>40':>4} {'>40/1k':>7} {'dash/1k':>8} {'longest':>8}")
    tot = dict(words=0, sents=0, over30=0, over40=0, dashes=0)
    for m in rows:
        k = m['words'] / 1000
        print(f"{m['path'][-48:]:48} {m['words']:6d} {m['mean']:5.1f} {m['over30']:4d} {m['over40']:4d} "
              f"{m['over40']/k:7.1f} {m['dashes']/k:8.1f} {m['longest']:8d}")
        for key in tot:
            tot[key] += m[key]
    k = tot['words'] / 1000
    print(f"{'TOTAL':48} {tot['words']:6d} {tot['words']/tot['sents']:5.1f} {tot['over30']:4d} {tot['over40']:4d} "
          f"{tot['over40']/k:7.1f} {tot['dashes']/k:8.1f}")
