"""Tests for NLP JSON parsing utilities."""
import json
import pytest

# Import only the pure function (no LLM dependency)
from app.services.nlp_json_utils import _strip_markdown_json


class TestStripMarkdownJson:
    """Tests for _strip_markdown_json helper."""

    def test_plain_json(self):
        """Plain JSON string passes through unchanged."""
        input_text = '{"key": "value"}'
        result = _strip_markdown_json(input_text)
        assert result == '{"key": "value"}'

    def test_json_code_block(self):
        """JSON wrapped in ```json code block is extracted."""
        input_text = '```json\n{"key": "value"}\n```'
        result = _strip_markdown_json(input_text)
        parsed = json.loads(result)
        assert parsed["key"] == "value"

    def test_generic_code_block(self):
        """JSON wrapped in ``` code block (no language) is extracted."""
        input_text = '```\n{"key": "value"}\n```'
        result = _strip_markdown_json(input_text)
        parsed = json.loads(result)
        assert parsed["key"] == "value"

    def test_whitespace_padding(self):
        """Whitespace around JSON is trimmed."""
        input_text = '  \n  {"key": "value"}  \n  '
        result = _strip_markdown_json(input_text)
        assert result == '{"key": "value"}'

    def test_empty_string(self):
        """Empty string returns empty string."""
        assert _strip_markdown_json("") == ""

    def test_none_input(self):
        """None input returns empty string."""
        assert _strip_markdown_json(None) == ""

    def test_nested_code_blocks(self):
        """Multiple code block markers are handled."""
        input_text = '```json\n{"nested": true}\n```\nextra text\n```'
        result = _strip_markdown_json(input_text)
        # Should extract content between first pair of ```
        parsed = json.loads(result)
        assert parsed["nested"] is True

    def test_code_block_with_complex_json(self):
        """Complex JSON in code block is correctly extracted."""
        complex_json = '{"summary": "Test", "items": [{"id": 1}, {"id": 2}]}'
        input_text = f'```json\n{complex_json}\n```'
        result = _strip_markdown_json(input_text)
        parsed = json.loads(result)
        assert parsed["summary"] == "Test"
        assert len(parsed["items"]) == 2
