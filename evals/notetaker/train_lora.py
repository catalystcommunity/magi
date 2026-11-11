"""Train a LoRA adapter for the note taker (README.md, "Training").

  python -I evals/notetaker/train_lora.py --base DIR_OR_REPO --data 'DIR/train-*.jsonl' \
      --out OUT_DIR [--init-adapter DIR] [--max-steps N] [--epochs E] [--lr LR] [--rank R] [--save-steps N] [--save-limit K]

The data are the chat records of `notes-eval -export` (system prompt, the
input of the note taker, the target answer). The loss counts only the
answer. The base loads in 4 bits (QLoRA), so a 12B model trains on a
16 GB GPU. The adapter goes to OUT_DIR; convert it for llama.cpp with
convert_lora_to_gguf.py and serve it with `llama-server --lora`.
"""

import argparse
import glob
import json

import torch
from datasets import Dataset
from peft import LoraConfig
from transformers import AutoModelForCausalLM, AutoTokenizer, BitsAndBytesConfig
from trl import SFTConfig, SFTTrainer


def records(pattern):
    out = []
    for f in sorted(glob.glob(pattern)):
        for line in open(f):
            m = json.loads(line)["messages"]
            # prompt: system + user; completion: the answer.
            out.append({"prompt": m[:2], "completion": m[2:]})
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", required=True)
    ap.add_argument("--data", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--epochs", type=float, default=1.0)
    ap.add_argument("--max-steps", type=int, default=-1)
    ap.add_argument("--lr", type=float, default=1e-4)
    ap.add_argument("--rank", type=int, default=16)
    ap.add_argument("--max-length", type=int, default=3072)
    ap.add_argument("--accum", type=int, default=16)
    ap.add_argument("--save-steps", type=int, default=100)
    ap.add_argument("--save-limit", type=int, default=2, help="checkpoints to keep (0: all)")
    ap.add_argument("--warmup", type=int, default=10, help="warmup steps")
    ap.add_argument("--dropout", type=float, default=0.05)
    ap.add_argument("--init-adapter", default="", help="continue the training of this adapter (a PEFT directory)")
    a = ap.parse_args()

    data = Dataset.from_list(records(a.data)).shuffle(seed=1)
    tok = AutoTokenizer.from_pretrained(a.base)
    model = AutoModelForCausalLM.from_pretrained(
        a.base,
        quantization_config=BitsAndBytesConfig(load_in_4bit=True, bnb_4bit_quant_type="nf4",
                                               bnb_4bit_compute_dtype=torch.bfloat16, bnb_4bit_use_double_quant=True),
        dtype=torch.bfloat16, device_map={"": 0}, attn_implementation="sdpa")
    lora = LoraConfig(r=a.rank, lora_alpha=2 * a.rank, lora_dropout=a.dropout, task_type="CAUSAL_LM",
                      target_modules=["q_proj", "k_proj", "v_proj", "o_proj", "gate_proj", "up_proj", "down_proj"])
    cfg = SFTConfig(output_dir=a.out, num_train_epochs=a.epochs, max_steps=a.max_steps, learning_rate=a.lr,
                    per_device_train_batch_size=1, gradient_accumulation_steps=a.accum, lr_scheduler_type="cosine",
                    warmup_steps=a.warmup, logging_steps=5, save_steps=a.save_steps,
                    save_total_limit=a.save_limit or None, bf16=True,
                    gradient_checkpointing=True, max_length=a.max_length, completion_only_loss=True,
                    report_to=[], seed=1)
    if a.init_adapter:
        # No prepare_model_for_kbit_training: it casts the bf16 weights to
        # fp32 and runs out of memory on a 16 GB GPU.
        from peft import PeftModel
        model.enable_input_require_grads()
        model = PeftModel.from_pretrained(model, a.init_adapter, is_trainable=True)
        lora = None
    trainer = SFTTrainer(model=model, args=cfg, train_dataset=data, processing_class=tok, peft_config=lora)
    trainer.train()
    trainer.save_model(a.out)
    tok.save_pretrained(a.out)
    print("adapter in", a.out)


if __name__ == "__main__":
    main()
