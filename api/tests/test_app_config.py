from aintercept_api.app import duckdb_memory_limit


def test_duckdb_gets_a_share_of_the_container():
    """The chart passes the container's limit; DuckDB's follows it unless set outright."""
    assert duckdb_memory_limit({"AINTERCEPT_CONTAINER_MEMORY": str(1536 * 2**20)}) == "921MB"
    assert duckdb_memory_limit({"AINTERCEPT_CONTAINER_MEMORY": "1610612736", "AINTERCEPT_MEMORY_LIMIT": "2GB"}) == "2GB"
    assert duckdb_memory_limit({}) is None
    assert duckdb_memory_limit({"AINTERCEPT_CONTAINER_MEMORY": "lots"}) is None
