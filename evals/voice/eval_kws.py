"""Keyword spotting of the persona names on the clips of dev and on LibriSpeech.
  venv/bin/python eval_kws.py MODEL_DIR boost threshold
"""
import glob, os, sys, re, collections
import numpy as np, soundfile as sf, sherpa_onnx
OWNER = os.environ['VOICE_EVAL_OWNER']  # a part of the subject of the owner of the voice
d = sys.argv[1]; boost = float(sys.argv[2]); thr = float(sys.argv[3])
import os as _os
names = _os.environ.get('KW', 'ARCHIMEDES,BEATRICE').split(',')
toks = sherpa_onnx.text2token(names, tokens=f'{d}/tokens.txt', tokens_type='bpe', bpe_model=f'{d}/bpe.model')
kw = f'/tmp/kw-{os.getpid()}.txt'
with open(kw, 'w') as f:
    for n, t in zip(names, toks):
        f.write(' '.join(t) + f' :{boost} #{thr} @' + n.replace(' ', '_') + '\n')
if len(sys.argv) > 4: print(open(kw).read())
enc = glob.glob(f'{d}/encoder*int8.onnx') or glob.glob(f'{d}/encoder*.onnx')
dec = [p for p in glob.glob(f'{d}/decoder*.onnx') if 'int8' not in p]
joi = glob.glob(f'{d}/joiner*int8.onnx') or glob.glob(f'{d}/joiner*.onnx')
ks = sherpa_onnx.KeywordSpotter(tokens=f'{d}/tokens.txt', encoder=enc[0], decoder=dec[0], joiner=joi[0], keywords_file=kw, num_threads=1)
def spot(x):
    s = ks.create_stream()
    x = np.concatenate([np.zeros(4800, 'float32'), x, np.zeros(16000, 'float32')])
    hits = []
    for i in range(0, len(x), 1600):
        s.accept_waveform(16000, x[i:i+1600])
        while ks.is_ready(s):
            ks.decode_stream(s)
            r = ks.get_result(s)
            if r:
                hits.append(r); ks.reset_stream(s)
    return hits
rows = {}
for line in open('utts.txt'):
    f = line.rstrip('\n').split('|'); rows[f[0]] = f
pos = collections.Counter(); tot = collections.Counter(); fa = collections.Counter(); secs = collections.Counter(); miss = []
for p in sorted(glob.glob('a16/*.wav')):
    b = os.path.basename(p)[:-4]; kind, mod, uid = b.split('-', 2); r = rows[uid]
    x, sr = sf.read(p, dtype='float32')
    text = r[6]
    named = re.match(r'\s*(hey|hi)?[ ,]*(archimedes|beatrice)', text, re.I)
    g = ('owner' if OWNER in r[3] else 'tts-person') if kind == 'human' else 'persona'
    h = spot(x)
    if named and kind == 'human':
        tot[g] += 1
        want = named.group(2).upper()
        if any(want in k for k in h): pos[g] += 1
        else: miss.append((g, text[:40], h))
    else:
        secs[g] += len(x) / 16000
        if h: fa[g] += 1; print('  false', g, h, '|', text[:60]) if len(sys.argv) > 4 else None
n = 0
for p in sorted(glob.glob('libri/LibriSpeech/dev-clean-2/*/*/*.flac')):
    x, sr = sf.read(p, dtype='float32'); secs['libri'] += len(x) / 16000
    if spot(x): fa['libri'] += 1
print(f"boost {boost} thr {thr}: " + ' '.join(f"{g} {pos[g]}/{tot[g]}" for g in tot) + ' | false: ' + ' '.join(f"{g} {fa[g]} in {secs[g]/60:.0f} min" for g in secs))
for m in miss: print('  miss', m)
