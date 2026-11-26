"""The owner in noise: verification score on the mixture, after a denoiser, and
after target-speaker extraction (VoxLock). Mixtures: babble of n LibriSpeech
speakers, and one other speaker, at some signal-to-noise ratios. Also the
same noise with no owner in it (must not pass)."""
import glob, os, sys, time, collections, random
import numpy as np, soundfile as sf, sherpa_onnx
OWNER = os.environ['VOICE_EVAL_OWNER']  # a part of the subject of the owner of the voice
random.seed(7); np.random.seed(7)
ECHO = ('The apprentice', 'Apprentice just started', 'We can take out', 'Yeah. Slash slash link')
rows = {l.split('|')[0]: l.rstrip('\n').split('|') for l in open('utts.txt')}
own = []
for p in sorted(glob.glob('a16/human-voice-*.wav')):
    r = rows[os.path.basename(p)[:-4].split('-', 2)[2]]
    if OWNER in r[3] and not r[6].startswith(ECHO):
        x, sr = sf.read(p, dtype='float32')
        if 2.0 <= len(x) / sr <= 9: own.append(x)
lib = collections.defaultdict(list)
for p in sorted(glob.glob('libri/LibriSpeech/dev-clean-2/*/*/*.flac')):
    lib[p.split('/')[-3]].append(p)
spk = sorted(lib)
def other(n):
    x, _ = sf.read(random.choice(lib[random.choice(spk)]), dtype='float32')
    while len(x) < n: x = np.concatenate([x, sf.read(random.choice(lib[random.choice(spk)]), dtype='float32')[0]])
    o = random.randint(0, len(x) - n); return x[o:o+n]
def rms(x): return float(np.sqrt(np.mean(x**2)) + 1e-9)
def speech_rms(x):
    n = 320; e = np.array([rms(x[i:i+n]) for i in range(0, len(x)-n, n)])
    return float(np.sqrt(np.mean(e[e > 0.3 * e.max()]**2)))
def mix(x, noise, snr):
    g = speech_rms(x) / (rms(noise) * 10 ** (snr / 20))
    y = x + g * noise
    return y / max(1.0, np.abs(y).max() / 0.98), g
ex = sherpa_onnx.SpeakerEmbeddingExtractor(sherpa_onnx.SpeakerEmbeddingExtractorConfig(model='models/3dspeaker_speech_eres2net_sv_en_voxceleb_16k.onnx', num_threads=2))
def emb(x):
    s = ex.create_stream(); s.accept_waveform(16000, x[:160000]); s.input_finished()
    v = np.array(ex.compute(s)); return v / np.linalg.norm(v)
# enrollment: the first 8 owner clips; test: the rest
enroll, test = own[:8], own[8:]
cen = np.mean([emb(x) for x in enroll], axis=0); cen /= np.linalg.norm(cen)
sample = np.concatenate(enroll)[:16000 * 20]
sf.write('/tmp/owner-enroll.wav', sample, 16000)
def denoiser(kind):
    mc = sherpa_onnx.OfflineSpeechDenoiserModelConfig(num_threads=4)
    if kind == 'gtcrn': mc.gtcrn = sherpa_onnx.OfflineSpeechDenoiserGtcrnModelConfig(model='models/gtcrn_simple.onnx')
    elif kind == 'dpdfnet': mc.dpdfnet = sherpa_onnx.OfflineSpeechDenoiserDpdfNetModelConfig(model='models/dpdfnet2.onnx')
    else: mc.voxlock = sherpa_onnx.OfflineSpeechDenoiserVoxLockModelConfig(enroll_model=f'models/{kind}/enroll.onnx',
            chunk_model=glob.glob(f'models/{kind}/chunk*.onnx')[0], voice_sample='/tmp/owner-enroll.wav')
    return sherpa_onnx.OfflineSpeechDenoiser(sherpa_onnx.OfflineSpeechDenoiserConfig(model=mc))
dn = {}
for k in sys.argv[1].split(','):
    try: dn[k] = denoiser(k)
    except Exception as e: print('no', k, str(e)[:200])
def run(d, y):
    t = time.time(); o = d.run(y, 16000); x = np.array(o.samples, dtype='float32')
    if o.sample_rate != 16000:
        import math
        idx = np.arange(0, len(x), o.sample_rate / 16000); x = np.interp(idx, np.arange(len(x)), x).astype('float32')
    return x, time.time() - t
print('owner test clips', len(test), 'clean scores', ' '.join(f"{float(emb(x) @ cen):.2f}" for x in test))
conds = [('babble6', 6, s) for s in (15, 10, 5, 0)] + [('one-other', 1, s) for s in (10, 5, 0, -5)]
os.makedirs('mixes', exist_ok=True)
for name, n, snr in conds:
    res = collections.defaultdict(list); neg = collections.defaultdict(list); rt = collections.defaultdict(list)
    for i, x in enumerate(test):
        noise = sum(other(len(x)) for _ in range(n))
        y, g = mix(x, noise, snr)
        only = np.clip(g * noise, -1, 1).astype('float32')
        if i == 0: sf.write(f'mixes/{name}-{snr}.wav', y, 16000)
        res['mix'].append(float(emb(y) @ cen)); neg['mix'].append(float(emb(only) @ cen))
        for k, d in dn.items():
            o, t = run(d, y); rt[k].append(t / (len(y) / 16000))
            if i == 0: sf.write(f'mixes/{name}-{snr}-{k}.wav', o, 16000)
            res[k].append(float(emb(o) @ cen)) if rms(o) > 1e-4 else res[k].append(-1.0)
            o2, _ = run(d, only)
            neg[k].append(float(emb(o2) @ cen) if rms(o2) > 1e-3 else -1.0)
    print(f"{name} SNR {snr:3d} dB: " + ' | '.join(f"{k}: owner min {min(v):.2f} med {np.median(v):.2f}, no-owner max {max(neg[k]):.2f}" + (f", rt {np.mean(rt[k]):.2f}" if k in rt else '') for k, v in res.items()), flush=True)
