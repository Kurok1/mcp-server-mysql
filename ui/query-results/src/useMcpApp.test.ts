/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 2.0.1
 */
import { describe, expect, it } from "vitest";
import { toolInputFromEvent } from "./useMcpApp";

describe("MCP tool-input bridge", () => {
  it("forwards both required mysql_query arguments", () => {
    expect(toolInputFromEvent({ arguments: { profile: "reporting", sql: "SELECT 1" } })).toEqual({
      profile: "reporting",
      sql: "SELECT 1",
    });
  });

  it("rejects a missing or blank profile instead of selecting a default", () => {
    expect(toolInputFromEvent({ arguments: { sql: "SELECT 1" } })).toBeNull();
    expect(toolInputFromEvent({ arguments: { profile: " ", sql: "SELECT 1" } })).toBeNull();
  });
});
