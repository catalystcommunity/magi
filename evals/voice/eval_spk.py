"""Speaker verification on the clips of dev: owner vs personas (TTS), TTS test
people, and LibriSpeech speakers. Prints equal error rates and the scores of
the clips that the transcript marks as echo of a persona.

  venv/bin/python eval_spk.py [seconds]   (seconds: cut each test clip, 0 = whole)
"""
import glob, os, sys, time, collections
import numpy as np, soundfile as sf, sherpa_onnx

# The subject of the person whose voice is the owner (a part of it is enough).
OWNER = os.environ['VOICE_EVAL_OWNER']

CUT = float(sys.argv[1]) if len(sys.argv) > 1 else 0
MODELS = [m for m in sorted(glob.glob('models/*.onnx')) if any(k in m for k in ('3dspeaker', 'wespeaker', 'nemo'))]
ECHO = ('The apprentice', 'Apprentice just started', 'We can take out', 'Yeah. Slash slash link')

rows = {}
for line in open('utts.txt'):
    f = line.rstrip('\n').split('|')
    rows[f[0]] = f

def load(p):
    x, sr = sf.read(p, dtype='float32')
    assert sr == 16000
    return x

def trim(x, top=0.02):
    """Drops the silence at both ends (energy), so a cut has speech."""
    n = 320
    e = np.array([np.sqrt(np.mean(x[i:i+n]**2)) for i in range(0, len(x)-n, n)])
    if len(e) == 0: return x
    on = np.where(e > max(top * e.max(), 1e-4) * 5)[0]
    if len(on) == 0: return x
    return x[on[0]*n:(on[-1]+1)*n]

groups = collections.defaultdict(list)   # name -> [(id, samples, text)]
for p in sorted(glob.glob('a16/*.wav')):
    b = os.path.basename(p)[:-4]
    kind, mod, uid = b.split('-', 2)
    r = rows[uid]
    x = trim(load(p))
    if len(x) < 8000: continue
    if kind == 'human':
        if OWNER in r[3]:
            g = 'echo?' if r[6].startswith(ECHO) else 'owner'
        else:
            g = 'tts-person'
    elif kind == 'persona':
        g = r[3]
    else:
        continue
    groups[g].append((uid, x, r[6][:50]))

libri = collections.defaultdict(list)
for p in sorted(glob.glob('libri/LibriSpeech/dev-clean-2/*/*/*.flac')):
    spk = p.split('/')[-3]
    if len(libri[spk]) < 6:
        x, sr = sf.read(p, dtype='float32'); assert sr == 16000
        libri[spk].append(x)

print({k: len(v) for k, v in groups.items()}, 'libri speakers', len(libri))

def cut(x):
    return x if CUT <= 0 else x[:int(CUT * 16000)]

def eer(pos, neg):
    pos, neg = np.sort(pos), np.sort(neg)
    best = (1, 0, 0)
    for t in np.unique(np.concatenate([pos, neg])):
        frr = np.mean(pos < t); far = np.mean(neg >= t)
        if abs(frr - far) < best[0]: best = (abs(frr - far), (frr + far) / 2, t)
    return best[1], best[2]

for m in MODELS:
    ex = sherpa_onnx.SpeakerEmbeddingExtractor(sherpa_onnx.SpeakerEmbeddingExtractorConfig(model=m, num_threads=2))
    def emb(x):
        s = ex.create_stream(); s.accept_waveform(16000, x[:16000*10]); s.input_finished()
        v = np.array(ex.compute(s)); return v / np.linalg.norm(v)
    t0 = time.time()
    own_full = [emb(x) for _, x, _ in groups['owner']]
    own_cut = [emb(cut(x)) for _, x, _ in groups['owner']]
    ms = (time.time() - t0) * 1000 / (2 * len(own_full))
    # leave one out: the enrollment is the mean of the other owner clips (whole clips)
    pos = []
    for i in range(len(own_full)):
        c = np.mean([e for j, e in enumerate(own_full) if j != i], axis=0); c /= np.linalg.norm(c)
        pos.append(float(own_cut[i] @ c))
    cen = np.mean(own_full, axis=0); cen /= np.linalg.norm(cen)
    neg = {}
    for g, items in groups.items():
        if g in ('owner', 'echo?'): continue
        neg[g] = [float(emb(cut(x)) @ cen) for _, x, _ in items]
    neg['libri'] = [float(emb(cut(x)) @ cen) for xs in libri.values() for x in xs]
    alln = np.concatenate([np.array(v) for v in neg.values() if len(v)])
    e, t = eer(np.array(pos), alln)
    print(f"\n== {os.path.basename(m)}  {ms:.0f} ms/clip  dim {len(cen)}")
    print(f"   owner scores: min {min(pos):.2f} p10 {np.percentile(pos,10):.2f} median {np.median(pos):.2f}   EER {e*100:.1f}% at {t:.2f}")
    for g, v in neg.items():
        if len(v): print(f"   {g[:28]:28s} n={len(v):4d} max {max(v):.2f} p99 {np.percentile(v,99):.2f} median {np.median(v):.2f}")
    # the persona voice prints: is an echo clip nearer to a persona than to the owner?
    pcs = {}
    for g, items in groups.items():
        if g.startswith('persona') and len(items) >= 5:
            es = [emb(x) for _, x, _ in items[:40]]
            c = np.mean(es, axis=0); pcs[g] = c / np.linalg.norm(c)
    for uid, x, text in groups['echo?']:
        v = emb(cut(x))
        best = max(pcs.items(), key=lambda kv: float(v @ kv[1])) if pcs else ('', cen)
        print(f"   echo? owner {float(v @ cen):.2f}  {best[0][:22]} {float(v @ best[1]):.2f}  | {text}")
    # owner clips against the persona voice prints (must stay low)
    if pcs:
        worst = max(float(e @ c) for e in own_full for c in pcs.values())
        print(f"   owner vs persona prints: max {worst:.2f}")
