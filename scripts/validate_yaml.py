from pathlib import Path

import yaml


ROOT = Path(__file__).resolve().parents[1]
FILES = [ROOT / "compose.yaml", *sorted((ROOT / "deploy").glob("*.yaml")), *sorted((ROOT / ".github").rglob("*.yml"))]


def main() -> None:
    for path in FILES:
        documents = [doc for doc in yaml.safe_load_all(path.read_text(encoding="utf-8")) if doc is not None]
        if not documents or not all(isinstance(doc, dict) for doc in documents):
            raise ValueError(f"{path.relative_to(ROOT)} must contain YAML mapping documents")
        print(f"YAML OK: {path.relative_to(ROOT)}")


if __name__ == "__main__":
    main()
