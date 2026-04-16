from fastapi import FastAPI
from pydantic import BaseModel, Field


app = FastAPI(title="Atlas Search AI Layer", version="0.1.0")


class SummaryRequest(BaseModel):
    query: str = Field(..., min_length=1)
    snippets: list[str] = Field(default_factory=list)


class SummaryResponse(BaseModel):
    query: str
    summary: str
    grounded_points: list[str]


@app.get("/healthz")
def healthcheck() -> dict[str, str]:
    return {"status": "ok"}


@app.post("/summarize", response_model=SummaryResponse)
def summarize(request: SummaryRequest) -> SummaryResponse:
    snippets = [snippet.strip() for snippet in request.snippets if snippet.strip()]
    grounded_points = snippets[:3]

    if grounded_points:
        summary = (
            f"Atlas Search found {len(snippets)} relevant snippets for '{request.query}'. "
            f"Top evidence suggests: {' | '.join(grounded_points)}"
        )
    else:
        summary = f"No grounded snippets are available yet for '{request.query}'."

    return SummaryResponse(
        query=request.query,
        summary=summary,
        grounded_points=grounded_points,
    )

