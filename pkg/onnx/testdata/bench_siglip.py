#!/usr/bin/env python3
"""Honest throughput measurement for the exported SigLIP2 vision tower.

The first run measured 627 ms/frame, but the CPU sits at 800 MHz idle under the
powersave governor (max 4800 MHz), so that number was meaningless. This warms up
properly, samples the real clock DURING the run, and compares fp32 against an
int8-quantized graph.
"""
from __future__ import annotations

import json
import pathlib
import threading
import time

import numpy as np
import onnxruntime as ort

HERE = pathlib.Path(__file__).parent / "siglip"
FP32 = HERE / "siglip2_vision_legacy.onnx"
INT8 = HERE / "siglip2_vision_int8.onnx"
SIDE = 224


def mean_mhz() -> float:
    freqs = []
    with open("/proc/cpuinfo") as fh:
        for line in fh:
            if line.startswith("cpu MHz"):
                freqs.append(float(line.split(":")[1]))
    return sum(freqs) / len(freqs) if freqs else 0.0


class ClockSampler(threading.Thread):
    """Sample CPU clock while the benchmark runs."""

    def __init__(self):
        super().__init__(daemon=True)
        self.samples: list[float] = []
        self.stop = threading.Event()

    def run(self):
        while not self.stop.wait(0.1):
            self.samples.append(mean_mhz())


def bench(path: pathlib.Path, label: str, threads: int, batch: int = 8,
          seconds: float = 12.0) -> dict | None:
    if not path.exists():
        print(f"[{label}] missing {path.name}, skipped")
        return None

    opts = ort.SessionOptions()
    opts.intra_op_num_threads = threads
    sess = ort.InferenceSession(str(path), opts, providers=["CPUExecutionProvider"])

    rng = np.random.default_rng(11)
    x = rng.standard_normal((batch, 3, SIDE, SIDE)).astype(np.float32)

    # Warm up long enough for the governor to ramp.
    warm_end = time.time() + 4.0
    while time.time() < warm_end:
        sess.run(["image_embeds"], {"pixel_values": x})

    sampler = ClockSampler()
    sampler.start()
    frames = 0
    t0 = time.time()
    while time.time() - t0 < seconds:
        sess.run(["image_embeds"], {"pixel_values": x})
        frames += batch
    elapsed = time.time() - t0
    sampler.stop.set()
    sampler.join()

    per_frame_ms = elapsed / frames * 1000
    clk = max(sampler.samples) if sampler.samples else 0.0
    scene_s = 900 * per_frame_ms / 1000  # 30-min scene at frame_interval=2

    print(f"[{label}] threads={threads} {per_frame_ms:6.1f} ms/frame  "
          f"peak clock {clk:.0f} MHz  -> {scene_s/60:.1f} min per 30-min scene")

    return {
        "label": label, "threads": threads,
        "ms_per_frame": round(per_frame_ms, 1),
        "peak_mhz": round(clk),
        "minutes_per_30min_scene": round(scene_s / 60, 2),
    }


def quantize() -> None:
    if INT8.exists():
        return
    from onnxruntime.quantization import QuantType, quantize_dynamic

    print("quantizing to int8 (dynamic) ...", flush=True)
    t0 = time.time()
    quantize_dynamic(str(FP32), str(INT8), weight_type=QuantType.QInt8)
    print(f"  {INT8.stat().st_size/1e6:.0f} MB in {time.time()-t0:.0f}s")


def main() -> int:
    print(f"idle clock {mean_mhz():.0f} MHz\n")

    results = []
    for threads in (6, 12):
        r = bench(FP32, "fp32", threads)
        if r:
            results.append(r)

    try:
        quantize()
        r = bench(INT8, "int8", 6)
        if r:
            results.append(r)
    except Exception as exc:
        print(f"int8 quantization failed: {str(exc)[:200]}")

    (HERE / "bench_report.json").write_text(json.dumps(results, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
