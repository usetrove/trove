cat > slug.py <<'EOF'
def slugify(text):
    return "-".join(text.lower().split())
EOF
