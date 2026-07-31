#!/usr/bin/env python3
"""SigLIP2 vision-tower export, take 2.

Take 1 (torch's dynamo exporter) produced a graph whose attention-pooling head
baked batch=1 into a Reshape, so batch=2 failed at inference. This tries the
strategies in order of preference and reports which actually yields a working
dynamic-batch graph.
"""
from __future__ import annotations

import json
import pathlib
import time
import warnings

import numpy as np
import onnxruntime as ort
import torch

warnings.filterwarnings("ignore")

OUT = pathlib.Path(__file__).parent / "siglip"
OUT.mkdir(exist_ok=True)
MODEL_ID = "google/siglip2-base-patch16-224"
SIDE = 224


class Wrapper(torch.nn.Module):
    def __init__(self, m):
        super().__init__()
        self.m = m

    def forward(self, pixel_values):
        return self.m(pixel_values=pixel_values).pooler_output


def check(path: pathlib.Path, wrapper, label: str) -> dict | None:
    """Run the exported graph at several batch sizes and compare to torch."""
    try:
        sess = ort.InferenceSession(str(path), providers=["CPUExecutionProvider"])
    except Exception as exc:
        print(f"  [{label}] session failed: {exc}")
        return None

    rng = np.random.default_rng(7)
    results = {}
    for b in (1, 2, 8):
        x = rng.standard_normal((b, 3, SIDE, SIDE)).astype(np.float32)
        try:
            got = sess.run(["image_embeds"], {"pixel_values": x})[0]
        except Exception as exc:
            print(f"  [{label}] batch {b} FAILED: {str(exc)[:120]}")
            return None
        with torch.no_grad():
            ref = wrapper(torch.from_numpy(x)).numpy()
        diff = float(np.abs(ref - got).max())
        results[b] = diff
        print(f"  [{label}] batch {b}: shape {got.shape}, max abs diff {diff:.3e}")

    # Throughput at batch 8.
    x = rng.standard_normal((8, 3, SIDE, SIDE)).astype(np.float32)
    sess.run(["image_embeds"], {"pixel_values": x})
    t0 = time.time()
    iters = 5
    for _ in range(iters):
        sess.run(["image_embeds"], {"pixel_values": x})
    per_frame = (time.time() - t0) / (iters * 8)
    print(f"  [{label}] fp32 CPU: {per_frame*1000:.1f} ms/frame "
          f"-> {900*per_frame:.0f}s per 30-min scene at interval=2")

    return {"max_diffs": results, "ms_per_frame": round(per_frame * 1000, 2)}


def main() -> int:
    from transformers import SiglipVisionModel

    vision = SiglipVisionModel.from_pretrained(MODEL_ID).eval()
    wrapper = Wrapper(vision).eval()
    n_params = sum(p.numel() for p in vision.parameters())
    print(f"vision tower: {n_params/1e6:.1f}M params, embed dim "
          f"{vision.config.hidden_size}")

    report: dict = {
        "model": MODEL_ID,
        "vision_params_millions": round(n_params / 1e6, 1),
        "attempts": {},
    }

    # Strategy 1: legacy TorchScript exporter. It handles dynamic_axes by
    # symbolic tracing rather than shape specialisation, which is exactly the
    # failure mode take 1 hit.
    path = OUT / "siglip2_vision_legacy.onnx"
    print("\n[legacy] exporting with dynamo=False ...")
    t0 = time.time()
    try:
        torch.onnx.export(
            wrapper,
            (torch.zeros(2, 3, SIDE, SIDE),),  # batch 2 so 1 cannot be folded in
            str(path),
            input_names=["pixel_values"],
            output_names=["image_embeds"],
            dynamic_axes={"pixel_values": {0: "batch"},
                          "image_embeds": {0: "batch"}},
            opset_version=17,
            do_constant_folding=True,
            dynamo=False,
        )
        print(f"  exported in {time.time()-t0:.1f}s "
              f"-> {path.stat().st_size/1e6:.1f} MB")
        report["attempts"]["legacy"] = check(path, wrapper, "legacy")
    except Exception as exc:
        print(f"  export failed: {str(exc)[:300]}")
        report["attempts"]["legacy"] = None

    # Strategy 2: dynamo exporter, but declare the dynamic shape explicitly.
    if not report["attempts"].get("legacy"):
        path = OUT / "siglip2_vision_dynamo.onnx"
        print("\n[dynamo] exporting with dynamic_shapes ...")
        try:
            batch = torch.export.Dim("batch", min=1, max=64)
            torch.onnx.export(
                wrapper,
                (torch.zeros(2, 3, SIDE, SIDE),),
                str(path),
                input_names=["pixel_values"],
                output_names=["image_embeds"],
                dynamic_shapes={"pixel_values": {0: batch}},
                opset_version=17,
                dynamo=True,
            )
            print(f"  -> {path.stat().st_size/1e6:.1f} MB")
            report["attempts"]["dynamo_dynamic_shapes"] = check(path, wrapper, "dynamo")
        except Exception as exc:
            print(f"  export failed: {str(exc)[:300]}")
            report["attempts"]["dynamo_dynamic_shapes"] = None

    (OUT / "export_report.json").write_text(json.dumps(report, indent=2))
    ok = [k for k, v in report["attempts"].items() if v]
    print(f"\nworking exports: {ok or 'NONE'}")
    return 0 if ok else 1


if __name__ == "__main__":
    raise SystemExit(main())
