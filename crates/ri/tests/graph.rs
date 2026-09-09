//! Graph qualification against false absence, invalid inputs and order dependence.

use engorch_ri::graph::*;

fn nodes() -> Vec<Node> {
    ["a", "b", "c"]
        .into_iter()
        .map(|id| Node {
            scip: None,
            id: id.into(),
            kind: NodeKind::Symbol,
            path: Some("source.rs".into()),
        })
        .collect()
}

fn coverage() -> Coverage {
    Coverage {
        node: "a".into(),
        relation: Relation::References,
        direction: Direction::Outgoing,
        producer: "semantic-v1".into(),
        completeness: Completeness::Complete,
    }
}

fn edge(id: &str, to: &str) -> Edge {
    Edge {
        id: id.into(),
        from: "a".into(),
        to: to.into(),
        relation: Relation::References,
        producer: "semantic-v1".into(),
        quality: Quality::Declared,
    }
}

#[test]
fn path_search_respects_direction_cycles_and_exploration_bounds() {
    let first = edge("one", "b");
    let mut second = edge("two", "c");
    second.from = "b".into();
    let mut cycle = edge("cycle", "a");
    cycle.from = "b".into();
    let graph = Graph::build(nodes(), vec![second, cycle, first], vec![]).unwrap();
    let mut query = PathQuery {
        from: "a".into(),
        to: "c".into(),
        relation: Relation::References,
        direction: Direction::Outgoing,
        producer: "semantic-v1".into(),
        max_depth: 3,
        max_edges: 10,
    };
    let found = graph.path(&query).unwrap();
    assert!(found.found);
    assert_eq!(
        found
            .edges
            .iter()
            .map(|e| e.id.as_str())
            .collect::<Vec<_>>(),
        vec!["one", "two"]
    );
    query.max_depth = 1;
    let limited = graph.path(&query).unwrap();
    assert!(!limited.found && limited.truncated && !limited.absence_proven);
    query.max_depth = 3;
    query.max_edges = 1;
    let limited = graph.path(&query).unwrap();
    assert_eq!(limited.examined_edges, 1);
    assert!(limited.truncated);
    query.max_edges = 10;
    query.direction = Direction::Incoming;
    assert!(!graph.path(&query).unwrap().found);
    query.from = "c".into();
    query.to = "a".into();
    assert!(graph.path(&query).unwrap().found);
    query.producer = "other".into();
    assert!(!graph.path(&query).unwrap().found);
    query.to = "c".into();
    assert!(graph.path(&query).unwrap().found);
    query.to = "missing".into();
    assert!(graph.path(&query).is_err());
}

#[test]
fn absence_requires_every_coverage_dimension_to_match() {
    let graph = Graph::build(nodes(), vec![], vec![coverage()]).unwrap();
    assert_eq!(
        graph
            .coverage(
                "a",
                Relation::References,
                Direction::Outgoing,
                "semantic-v1"
            )
            .unwrap(),
        Some(Completeness::Complete)
    );
    assert_eq!(
        graph
            .coverage(
                "a",
                Relation::References,
                Direction::Incoming,
                "semantic-v1"
            )
            .unwrap(),
        None
    );
    assert!(
        graph
            .coverage(
                "missing",
                Relation::References,
                Direction::Outgoing,
                "semantic-v1"
            )
            .is_err()
    );
    assert!(
        graph
            .neighbors(
                "a",
                Relation::References,
                Direction::Outgoing,
                "semantic-v1"
            )
            .unwrap()
            .absence_proven
    );
    for (node, relation, direction, producer) in [
        (
            "b",
            Relation::References,
            Direction::Outgoing,
            "semantic-v1",
        ),
        ("a", Relation::Calls, Direction::Outgoing, "semantic-v1"),
        (
            "a",
            Relation::References,
            Direction::Incoming,
            "semantic-v1",
        ),
        (
            "a",
            Relation::References,
            Direction::Outgoing,
            "structural-v1",
        ),
    ] {
        let result = graph
            .neighbors(node, relation, direction, producer)
            .unwrap();
        assert_eq!(result.completeness, Completeness::Unknown);
        assert!(!result.absence_proven);
    }
    assert!(
        graph
            .neighbors(
                "missing",
                Relation::References,
                Direction::Outgoing,
                "semantic-v1"
            )
            .is_err()
    );
    let mut incomplete = coverage();
    incomplete.completeness = Completeness::Partial;
    let graph = Graph::build(nodes(), vec![], vec![incomplete]).unwrap();
    assert!(
        !graph
            .neighbors(
                "a",
                Relation::References,
                Direction::Outgoing,
                "semantic-v1"
            )
            .unwrap()
            .absence_proven
    );
}

#[test]
fn indexes_preserve_edge_quality_direction_and_stable_order() {
    let mut first = edge("z", "b");
    first.quality = Quality::Inferred;
    let graph = Graph::build(nodes(), vec![first, edge("a", "c")], vec![coverage()]).unwrap();
    let result = graph
        .neighbors(
            "a",
            Relation::References,
            Direction::Outgoing,
            "semantic-v1",
        )
        .unwrap();
    assert_eq!(
        result
            .edges
            .iter()
            .map(|e| e.id.as_str())
            .collect::<Vec<_>>(),
        ["a", "z"]
    );
    assert_eq!(result.edges[1].quality, Quality::Inferred);
    assert!(!result.absence_proven);
    assert_eq!(
        graph
            .neighbors(
                "b",
                Relation::References,
                Direction::Incoming,
                "semantic-v1"
            )
            .unwrap()
            .edges[0]
            .id,
        "z"
    );
    assert_eq!(
        graph
            .at_path("source.rs")
            .iter()
            .map(|n| n.id.as_str())
            .collect::<Vec<_>>(),
        ["a", "b", "c"]
    );
    assert_eq!(graph.node("a").unwrap().kind, NodeKind::Symbol);
}

#[test]
fn malformed_graph_cannot_publish_partial_indexes() {
    assert!(Graph::build(nodes(), vec![edge("e", "missing")], vec![]).is_err());
    assert!(Graph::build(nodes(), vec![edge("e", "a"), edge("e", "b")], vec![]).is_err());
    assert!(Graph::build(nodes(), vec![], vec![coverage(), coverage()]).is_err());
    let mut duplicated = nodes();
    duplicated.push(duplicated[0].clone());
    assert!(Graph::build(duplicated, vec![], vec![]).is_err());
    let mut invalid = nodes();
    invalid[0].path = Some("../outside".into());
    assert!(Graph::build(invalid, vec![], vec![]).is_err());
}
