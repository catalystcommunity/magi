"""Train the turn classifier and export it to ONNX.

  python train.py OUT_DIR [--base sentence-transformers/all-MiniLM-L12-v2]
                  [--revision REV] [--epochs 3] [--smooth 0.1] [--lr 5e-5]
                  [--device auto|cpu|cuda]
  python train.py OUT_DIR --export-only    (export the saved model again)

OUT_DIR gets model.onnx (fp32), model-int8.onnx (dynamic int8),
tokenizer.json, config.json (labels, max length, inputs), hf/ and tok/ (the
trained model), and report.json (metrics on heldout.py). Training uses a CUDA GPU
when torch sees one; on a CPU it is slower (see primitives/speech/README.md). The engine uses
model-int8.onnx as turn/model.onnx: on MiniLM it is faster and took no
held-out turn as a backchannel (see the report in
primitives/speech/README.md). Needs torch, transformers 4.x (5.x does not
load deberta v3 weights), sentencepiece, onnx, onnxruntime. The engine
needs only onnxruntime, tokenizers, and numpy.

Base models that we compared (2026-11-04, same data): MiniLM-L12 (Apache-2.0,
33M parameters, trained on paraphrase data) generalizes better than
deberta-v3-xsmall (MIT) on the held-out sets, and is faster.
"""

import json
import random
import sys
import time
from pathlib import Path

import numpy as np
import torch
from transformers import AutoModelForSequenceClassification, AutoTokenizer

sys.path.insert(0, str(Path(__file__).parent))
import data  # noqa: E402
import heldout  # noqa: E402

LABELS = [data.ACK, data.TURN, data.UNCLEAR]
BASE = "sentence-transformers/all-MiniLM-L12-v2"
BASE_REVISION = "a50ef00143b4d5391434df20ae11632588ac25be"
DEVICE = "cpu"
MAX_LEN = 64


def encode(tok, rows):
    # The text of the person first: when the pair is too long, the start
    # of the context is cut, not the words of the person.
    return tok([r["text"] for r in rows], [r["context"] for r in rows], truncation="only_second",
               max_length=MAX_LEN, padding=True, return_tensors="pt")


def main():
    args = sys.argv[1:]
    out = Path(args[0])
    base = args[args.index("--base") + 1] if "--base" in args else BASE
    # The default revision is the one that made magi-turn-minilm-l12-v1.
    rev = args[args.index("--revision") + 1] if "--revision" in args else (BASE_REVISION if base == BASE else None)
    want = args[args.index("--device") + 1] if "--device" in args else "auto"
    global DEVICE
    DEVICE = ("cuda" if torch.cuda.is_available() else "cpu") if want == "auto" else want
    epochs = int(args[args.index("--epochs") + 1]) if "--epochs" in args else 3
    # Label smoothing keeps the model from 1.0 on everything, so that a
    # threshold on the probability means something.
    smooth = float(args[args.index("--smooth") + 1]) if "--smooth" in args else 0.1
    lr = float(args[args.index("--lr") + 1]) if "--lr" in args else 5e-5
    out.mkdir(parents=True, exist_ok=True)
    if "--export-only" in args:  # export the saved model again
        test = [dict(r, context=data.norm(r["context"]), text=data.norm(r["text"])) for r in heldout.make()]
        tok = AutoTokenizer.from_pretrained(out / "tok")
        model = AutoModelForSequenceClassification.from_pretrained(out / "hf").eval()
        exclude = {r["text"] for r in test} - {"yes", "yeah", "okay", "sure", "mm hmm", "right", "uh", "um", "so", "well", "no", "what"}
        train = data.make(80000, 1, exclude)
        return export(out, model, tok, test, data.make(3000, 2, exclude), {r["text"] for r in train})
    random.seed(0)
    torch.manual_seed(0)
    test = [dict(r, context=data.norm(r["context"]), text=data.norm(r["text"])) for r in heldout.make()]
    # The held-out texts are not in the training data, except the common
    # words that the context decides (yes, okay, um): those test the context.
    common = {"yes", "yeah", "okay", "sure", "mm hmm", "right", "uh", "um", "so", "well", "no", "what"}
    exclude = {r["text"] for r in test} - common
    train = data.make(80000, 1, exclude)
    dev = data.make(3000, 2, exclude)
    seen = {r["text"] for r in train}
    print("held-out texts also in training:", sum(r["text"] in seen for r in test), "of", len(test))

    print("device:", DEVICE, flush=True)
    t0 = time.monotonic()
    tok = AutoTokenizer.from_pretrained(base, revision=rev, use_fast=True)
    model = AutoModelForSequenceClassification.from_pretrained(base, revision=rev, num_labels=3).to(DEVICE)
    opt = torch.optim.AdamW(model.parameters(), lr=lr, weight_decay=0.01)
    bs = 64
    steps = epochs * len(train) // bs
    sched = torch.optim.lr_scheduler.OneCycleLR(opt, max_lr=lr, total_steps=steps, pct_start=0.1)
    y = {l: i for i, l in enumerate(LABELS)}
    step = 0
    for ep in range(epochs):
        random.shuffle(train)
        model.train()
        for i in range(0, len(train) - bs + 1, bs):
            b = train[i:i + bs]
            enc = {k: v.to(DEVICE) for k, v in encode(tok, b).items()}
            logits = model(**enc).logits
            loss = torch.nn.functional.cross_entropy(logits, torch.tensor([y[r["label"]] for r in b]).to(DEVICE),
                                                     label_smoothing=smooth)
            loss.backward()
            torch.nn.utils.clip_grad_norm_(model.parameters(), 1.0)
            opt.step(); sched.step(); opt.zero_grad()
            step += 1
            if step % 200 == 0:
                print(f"epoch {ep} step {step}/{steps} loss {loss.item():.4f} {time.monotonic() - t0:.0f} s", flush=True)
        print("dev", metrics(predict_torch(model, tok, dev), dev, 0.0))
        print("held-out", metrics(predict_torch(model, tok, test), test, 0.0))

    model.eval().cpu()
    model.save_pretrained(out / "hf")
    tok.save_pretrained(out / "tok")
    export(out, model, tok, test, dev, seen, base)


def export(out, model, tok, test, dev, seen, base=BASE):
    """ONNX (fp32 and int8), the config of the engine, and the report."""
    (out / "tokenizer.json").write_bytes((out / "tok" / "tokenizer.json").read_bytes())
    enc = encode(tok, test[:2])
    names = [n for n in ["input_ids", "attention_mask", "token_type_ids"] if n in enc]
    torch.onnx.export(model, tuple(enc[n] for n in names), out / "model.onnx", input_names=names,
                      output_names=["logits"], opset_version=17, dynamo=False,
                      dynamic_axes={**{n: {0: "batch", 1: "seq"} for n in names}, "logits": {0: "batch"}})
    import onnx
    # The export removes inputs that the model does not use (deberta v3
    # has no token types).
    names = [i.name for i in onnx.load(str(out / "model.onnx")).graph.input]
    from onnxruntime.quantization import QuantType, quantize_dynamic
    quantize_dynamic(str(out / "model.onnx"), str(out / "model-int8.onnx"), weight_type=QuantType.QInt8)
    (out / "config.json").write_text(json.dumps({"labels": LABELS, "max_length": MAX_LEN, "inputs": names,
                                                 "base": base, "order": "text, context"}, indent=1))
    novel = [r for r in test if r["text"] not in seen]
    report = {"heldout_items": len(test), "heldout_novel_items": len(novel)}
    for name in ["model.onnx", "model-int8.onnx"]:
        p, ms = predict_onnx(out / name, tok, names, test)
        nov = np.array([q for q, r in zip(p, test) if r["text"] not in seen])
        report[name] = {"heldout": {t: metrics(p, test, t) for t in [0.0, 0.8, 0.9, 0.95, 0.98]}, "ms_per_item": ms,
                        "heldout_novel": {t: metrics(nov, novel, t) for t in [0.0, 0.9, 0.95]},
                        "dev": metrics(predict_onnx(out / name, tok, names, dev[:1000])[0], dev[:1000], 0.0)}
        errs = [(r["context"][-40:], r["text"], r["label"], LABELS[int(np.argmax(q))], round(float(q.max()), 3))
                for r, q in zip(test, p) if LABELS[int(np.argmax(q))] != r["label"]]
        report[name]["errors"] = errs
    (out / "report.json").write_text(json.dumps(report, indent=1, default=float))
    print(json.dumps({k: {"heldout": v["heldout"], "novel": v["heldout_novel"], "ms": v["ms_per_item"]}
                      for k, v in report.items() if isinstance(v, dict)}, indent=1, default=int))


def predict_torch(model, tok, rows):
    model.eval()
    ps = []
    with torch.no_grad():
        for i in range(0, len(rows), 256):
            enc = {k: v.to(DEVICE) for k, v in encode(tok, rows[i:i + 256]).items()}
            ps.append(torch.softmax(model(**enc).logits, -1).cpu().numpy())
    model.train()
    return np.concatenate(ps)


def predict_onnx(path, tok, names, rows):
    import onnxruntime as ort
    so = ort.SessionOptions()
    so.intra_op_num_threads = 2
    sess = ort.InferenceSession(str(path), so, providers=["CPUExecutionProvider"])
    ps, t0 = [], time.perf_counter()
    for r in rows:  # one at a time, as in the engine
        enc = tok([r["text"]], [r["context"]], truncation="only_second", max_length=MAX_LEN, return_tensors="np")
        logits = sess.run(None, {n: enc[n].astype(np.int64) for n in names})[0]
        e = np.exp(logits - logits.max(-1, keepdims=True))
        ps.append((e / e.sum(-1, keepdims=True))[0])
    return np.array(ps), round((time.perf_counter() - t0) * 1000 / len(rows), 2)


def metrics(p, rows, threshold):
    """Accuracy, and the rates that matter: an "ack" decision (the persona
    goes on) needs P(ack) >= threshold; a real turn that is taken as ack
    is the worst error."""
    gold = [r["label"] for r in rows]
    pred = [LABELS[int(np.argmax(q))] for q in p]
    acc = sum(a == b for a, b in zip(gold, pred)) / len(rows)
    ack = [q[0] >= threshold and LABELS[int(np.argmax(q))] == "ack" for q in p]
    turns = [g == "turn" for g in gold]
    acks = [g == "ack" for g in gold]
    turn_as_ack = sum(a and t for a, t in zip(ack, turns))
    return {"accuracy": round(acc, 4),
            "ack_decisions": int(sum(ack)),
            "ack_precision": round(sum(a and g for a, g in zip(ack, acks)) / max(1, sum(ack)), 4),
            "ack_recall": round(sum(a and g for a, g in zip(ack, acks)) / max(1, sum(acks)), 4),
            "turns_taken_as_ack": int(turn_as_ack), "turns": sum(turns),
            "turn_recall": round(sum(pl == "turn" and t for pl, t in zip(pred, turns)) / max(1, sum(turns)), 4)}


if __name__ == "__main__":
    main()
